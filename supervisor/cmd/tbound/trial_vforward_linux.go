//go:build linux

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/researchroute"
	"tbound/supervisor/internal/vrun"
)

const vForwarderAnswerWait = 3 * time.Minute

type vForwarderHandle interface {
	Dir() string
	Finish() (researchroute.ForwardTotals, error)
	Abort()
}

type vForwarderProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  *bufio.Reader
	dir    string
	stderr *tailBuffer
	once   sync.Once
}

func startVForwarder(ctx context.Context, caseID, requestID string) (vForwarderHandle, error) {
	registration, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return nil, err
	}
	socket, _, err := serviceSocketRegistrationFromSignedRegistration(registration)
	if err != nil {
		return nil, err
	}
	if len(socket.AllowedClientUIDs) == 0 || socket.AllowedClientUIDs[0] <= 0 || socket.AllowedClientPrimaryGID <= 0 {
		return nil, errors.New("the service registration names no client identity")
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	stderr := &tailBuffer{max: 2048}
	cmd := vForwarderCommand(self, caseID, requestID)
	cmd.Env, cmd.Dir, cmd.Stderr = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}, "/", stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true,
		Credential: &syscall.Credential{Uid: uint32(socket.AllowedClientUIDs[0]), Gid: uint32(socket.AllowedClientPrimaryGID), Groups: []uint32{}}}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	process := &vForwarderProcess{cmd: cmd, stdin: stdin, lines: bufio.NewReader(stdout), stderr: stderr}
	response, err := process.answer(ctx)
	if err != nil || response.Status != piServiceStatusReady {
		process.Abort()
		if err == nil {
			err = errors.New(piServiceRefusalMessage(response.Status, response.ErrorCode))
		}
		return nil, fmt.Errorf("the research service did not open the forwarder: %w", err)
	}
	var ready struct {
		Dir string `json:"dir"`
	}
	if json.Unmarshal(response.Receipt, &ready) != nil || !vForwarderDirAcceptable(ready.Dir) {
		process.Abort()
		return nil, errors.New("the research service named an unusable forwarder directory")
	}
	process.dir = ready.Dir
	return process, nil
}

func vForwarderCommand(self, caseID, requestID string) *exec.Cmd {
	args := append(append([]string(nil), noNewPrivsWrap[1:]...), self, "serve", "--pi", "--task-id", caseID, "--attempt", "1", "--role", corpusRoleV, "--task-request-id", requestID, "--bash-no-outputs")
	return exec.Command(noNewPrivsWrap[0], args...)
}

func vForwarderDirAcceptable(dir string) bool {
	parent := filepath.Join(piruntime.D09ManagedPiRuntimeDir, vForwarderDirName)
	return filepath.IsAbs(dir) && filepath.Clean(dir) == dir && filepath.Dir(dir) == parent && !strings.ContainsAny(dir, ",=\x00")
}

func (p *vForwarderProcess) answer(ctx context.Context) (piServiceResponse, error) {
	type result struct {
		response piServiceResponse
		err      error
	}
	done := make(chan result, 1)
	go func() {
		line, err := p.lines.ReadBytes('\n')
		var response piServiceResponse
		if err == nil {
			err = json.Unmarshal(line, &response)
		}
		done <- result{response, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return r.response, fmt.Errorf("%w: %s", r.err, p.stderr.String())
		}
		return r.response, nil
	case <-ctx.Done():
		return piServiceResponse{}, ctx.Err()
	case <-time.After(vForwarderAnswerWait):
		return piServiceResponse{}, errors.New("the research service did not answer in time")
	}
}

func (p *vForwarderProcess) Dir() string { return p.dir }

func (p *vForwarderProcess) Finish() (researchroute.ForwardTotals, error) {
	_ = p.stdin.Close()
	response, err := p.answer(context.Background())
	p.Abort()
	if err != nil {
		return researchroute.ForwardTotals{}, err
	}
	var totals researchroute.ForwardTotals
	if response.Status != "completed" || json.Unmarshal(response.Receipt, &totals) != nil {
		return researchroute.ForwardTotals{}, errors.New("the research service did not report the unit's records: " + piServiceRefusalMessage(response.Status, response.ErrorCode))
	}
	return totals, nil
}

func (p *vForwarderProcess) Abort() {
	p.once.Do(func() {
		_ = p.stdin.Close()
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		_ = p.cmd.Wait()
	})
}

func checkVForwarderDir(dir string, hostUID uint32) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || stat.Uid != hostUID || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("the forwarder directory %s is not a 0700 directory of the service uid", dir)
	}
	for _, entry := range []struct {
		name, kind string
		fileType   os.FileMode
	}{{vrun.SocketName, "socket", os.ModeSocket}, {vrun.TokenName, "regular file", 0}} {
		info, err := os.Lstat(filepath.Join(dir, entry.name))
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode().Type() != entry.fileType || info.Mode().Perm() != 0o600 || stat.Uid != hostUID {
			return fmt.Errorf("%s in the forwarder directory is not a 0600 %s of the service uid", entry.name, entry.kind)
		}
	}
	return nil
}
