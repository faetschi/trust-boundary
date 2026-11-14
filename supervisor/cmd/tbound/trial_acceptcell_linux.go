package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"tbound/supervisor/internal/taskaccept"
	"tbound/supervisor/internal/vrun"
)

// cellNodeRunner runs the node_test checks of trial acceptance (`accept` and `v-run`) in an
// isolated acceptance cell: the signed cell image pinned by digest, no network, a read-only root,
// all capabilities dropped and no_new_privs, the unprivileged cell identity, a fixed timeout, and
// one read-only mount holding only the program (the agent's output file followed by the pinned
// dataset tests). It goes through the same podman seam as v-run. Every setup or podman failure is
// an error, which taskaccept turns into a refused acceptance: the agent's file never runs on the
// host and a broken cell never counts as a pass.
//
// The cell's behaviour on the guest (podman, crun, the image's node) is verified only on the guest,
// in the corpus-mode rehearsal; the tests here check the argv and the fail-closed handling.
type cellNodeRunner struct {
	ctx  context.Context
	task trialTaskEnv
}

func (r cellNodeRunner) RunNode(program []byte) (run taskaccept.NodeRun, err error) {
	ctx, task := r.ctx, r.task
	tmpl, err := task.template(ctx)
	if err != nil {
		return taskaccept.NodeRun{}, fmt.Errorf("acceptance cell: the registered cell cannot be resolved: %w", err)
	}
	if err := task.preflight(); err != nil {
		return taskaccept.NodeRun{}, fmt.Errorf("acceptance cell: %w", err)
	}
	token, err := task.token()
	if err != nil {
		return taskaccept.NodeRun{}, fmt.Errorf("acceptance cell: %w", err)
	}
	dir := filepath.Join(task.workRoot, "accept-"+token)
	if err := makeAcceptDir(task.workRoot, dir, program, tmpl.HostUID, tmpl.HostGID); err != nil {
		return taskaccept.NodeRun{}, errors.Join(fmt.Errorf("acceptance cell: %w", err), removeAcceptDir(dir))
	}
	defer func() {
		if rerr := removeAcceptDir(dir); rerr != nil {
			run, err = taskaccept.NodeRun{}, errors.Join(err, fmt.Errorf("acceptance cell: %s was not removed: %w", dir, rerr))
		}
	}()
	name := acceptCellPrefix + token
	args, err := vrun.AcceptPodmanArgs(vrun.AcceptSpec{Name: name, ImageRef: tmpl.ImageRef, CrunPath: tmpl.CrunPath,
		CheckDir: dir, UID: int(tmpl.CellUID), GID: int(tmpl.CellGID), PidsMax: tmpl.PidsMax, MemoryMB: tmpl.MemoryMB, CPUs: tmpl.CPUs})
	if err != nil {
		return taskaccept.NodeRun{}, err
	}
	stdout, stderr := &tailBuffer{max: taskaccept.MaxNodeStdout}, &tailBuffer{max: 2048}
	inv := podmanInvocation{Path: tmpl.PodmanPath, Args: args, Env: vPodmanEnv(), Dir: dir, UID: tmpl.HostUID, GID: tmpl.HostGID,
		Stdout: stdout, Stderr: stderr}
	// The cell limit is podman's --timeout (task.deadline maps seconds to a duration, as for v-run).
	limit := task.deadline(vrun.AcceptTimeoutSeconds)
	runCtx, cancel := context.WithTimeout(ctx, limit+task.killGrace)
	started := time.Now()
	exit, runErr := task.podman.Run(runCtx, inv)
	elapsed := time.Since(started)
	hitDeadline := errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	cancel()
	// The container is --rm, but a killed podman can leave it behind: remove it by force always.
	rmErr := removeVContainer(ctx, task, tmpl, name, dir)
	switch {
	case ctx.Err() != nil:
		return taskaccept.NodeRun{}, ctx.Err()
	case rmErr != nil:
		return taskaccept.NodeRun{}, fmt.Errorf("acceptance cell: %w", rmErr)
	case hitDeadline:
		// podman did not end by itself a grace after its own --timeout (e.g. a storage lock): the
		// cell is broken, not the tests slow, so acceptance is refused.
		return taskaccept.NodeRun{}, fmt.Errorf("acceptance cell: podman did not exit within %s of its own timeout", task.killGrace)
	case runErr != nil:
		return taskaccept.NodeRun{}, fmt.Errorf("acceptance cell: podman could not be run: %w", runErr)
	case exit >= 125 && exit <= 127:
		return taskaccept.NodeRun{}, fmt.Errorf("acceptance cell: podman failed (exit %d): %s", exit, stderr.String())
	}
	return taskaccept.NodeRun{Exit: exit, TimedOut: exit == vTimedOutExit && elapsed >= limit, Stdout: stdout.buf}, nil
}

// removeAcceptDir removes the check directory; it was made read-only (0500) for the cell, so its
// write bit comes back first.
func removeAcceptDir(dir string) error {
	if err := os.Chmod(dir, 0o700); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.RemoveAll(dir)
}

// makeAcceptDir makes dir, a new private directory under workRoot holding only the program file,
// and hands both to the host identity podman runs as; the cell mounts it read-only.
func makeAcceptDir(workRoot, dir string, program []byte, uid, gid uint32) error {
	if err := os.MkdirAll(workRoot, 0o711); err != nil {
		return err
	}
	if err := os.Chmod(workRoot, 0o711); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	file := filepath.Join(dir, vrun.AcceptProgram)
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return err
	}
	if _, err := f.Write(program); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return err
	}
	for _, p := range []string{file, dir} {
		if err := os.Lchown(p, int(uid), int(gid)); err != nil {
			return fmt.Errorf("give %s to the cell identity: %w", p, err)
		}
	}
	return os.Chmod(dir, 0o500)
}
