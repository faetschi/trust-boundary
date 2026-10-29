//go:build linux

package projectbundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Git encodes binary patches and quoted paths in an isolated object store and index.
// No repository objects, index entries, filters or hooks from the user's tree are used.
func writePatch(bundle io.ReadSeeker, offsets []int64, root *os.File, worktree string, store *applyState, before, target Manifest, header ExportHeader) ([]string, error) {
	nonEmpty := func(m Manifest) map[string]bool {
		out := make(map[string]bool)
		for _, e := range m.Entries {
			if e.Type != TypeFile {
				continue
			}
			for parent := filepath.Dir(e.Path); parent != "."; parent = filepath.Dir(parent) {
				out[parent] = true
			}
		}
		return out
	}
	beforeFiles, afterFiles := nonEmpty(before), nonEmpty(target)
	for _, c := range header.Changes {
		if c.Type == TypeDir && (c.Op == OpCreate && !afterFiles[c.Path] || c.Op == OpDelete && !beforeFiles[c.Path]) {
			return nil, fmt.Errorf("projectbundle: Git patches cannot represent empty directory change %s; use direct mode with writer exclusion", c.Path)
		}
	}
	for _, e := range target.Entries {
		if e.Type == TypeDir && !afterFiles[e.Path] && beforeFiles[e.Path] {
			return nil, fmt.Errorf("projectbundle: Git patches cannot retain empty directory %s; use direct mode with writer exclusion", e.Path)
		}
	}
	scratch, err := os.MkdirTemp("", "tbound-patch-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	run := func(input io.Reader, args ...string) ([]byte, error) {
		cmd := exec.Command("git", append([]string{"-C", scratch}, args...)...)
		cmd.Env = patchGitEnv()
		cmd.Stdin = input
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("projectbundle: git %s: %w: %s", args[0], err, stderr.String())
		}
		return out, nil
	}
	if _, err := run(nil, "init", "--bare", "--quiet", "--object-format=sha1"); err != nil {
		return nil, err
	}
	states := make(map[string]Entry, len(before.Entries))
	for _, e := range before.Entries {
		states[e.Path] = e
	}
	mode := func(executable bool) string {
		if executable {
			return "100755"
		}
		return "100644"
	}
	var index bytes.Buffer
	oldHashes := make(map[string]string)
	for _, c := range header.Changes {
		old, ok := states[c.Path]
		if !ok || old.Type != TypeFile {
			continue
		}
		var content bytes.Buffer
		if err := copyFile(root, old, &content); err != nil {
			return nil, err
		}
		hash, err := run(&content, "hash-object", "-w", "--stdin")
		if err != nil {
			return nil, err
		}
		oid := strings.TrimSpace(string(hash))
		oldHashes[c.Path] = oid
		fmt.Fprintf(&index, "%s %s\t%s%c", mode(old.Executable), oid, c.Path, 0)
	}
	if _, err := run(&index, "update-index", "-z", "--index-info"); err != nil {
		return nil, err
	}
	tree, err := run(nil, "write-tree")
	if err != nil {
		return nil, err
	}
	index.Reset()
	var paths []string
	for i, c := range header.Changes {
		paths = append(paths, c.Path)
		if c.Type != TypeFile {
			continue
		}
		if c.Op == OpDelete {
			fmt.Fprintf(&index, "0 %s\t%s%c", strings.Repeat("0", 40), c.Path, 0)
			continue
		}
		oid := oldHashes[c.Path]
		if c.hasContent() {
			if _, err := bundle.Seek(offsets[i], io.SeekStart); err != nil {
				return nil, err
			}
			var content bytes.Buffer
			h := sha256.New()
			n, err := io.Copy(io.MultiWriter(&content, h), io.LimitReader(bundle, c.Size))
			if err != nil {
				return nil, err
			}
			if n != c.Size || hex.EncodeToString(h.Sum(nil)) != c.SHA256 {
				return nil, fmt.Errorf("projectbundle: content of %s changed after validation", c.Path)
			}
			hash, err := run(&content, "hash-object", "-w", "--stdin")
			if err != nil {
				return nil, err
			}
			oid = strings.TrimSpace(string(hash))
		}
		fmt.Fprintf(&index, "%s %s\t%s%c", mode(c.Executable), oid, c.Path, 0)
	}
	if _, err := run(&index, "update-index", "-z", "--index-info"); err != nil {
		return nil, err
	}
	patch, err := run(nil, "-c", "core.quotePath=true", "diff", "--cached", "--binary", "--full-index", "--no-renames", "--no-ext-diff", "--no-textconv", strings.TrimSpace(string(tree)), "--")
	if err != nil {
		return nil, err
	}
	if len(patch) == 0 {
		return nil, fmt.Errorf("projectbundle: directory-only changes cannot be represented by a Git patch; use direct mode with writer exclusion")
	}
	if err := checkGit(worktree, header.Changes); err != nil {
		return nil, err
	}
	for _, c := range header.Changes {
		if old, ok := states[c.Path]; ok && old.Type == TypeFile {
			f, err := openBeneath(root, c.Path, unix.O_RDONLY|unix.O_NONBLOCK)
			if err != nil {
				return nil, err
			}
			sum, e := hashFile(f, old.Size)
			f.Close()
			if e != nil || sum != old.SHA256 {
				return nil, fmt.Errorf("projectbundle: target %s changed during patch generation", c.Path)
			}
		}
	}
	cmd := exec.Command("git", "-C", worktree, "-c", "core.fsmonitor=false", "apply", "--check", "--binary", "-")
	cmd.Env = patchGitEnv()
	cmd.Stdin = bytes.NewReader(patch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("projectbundle: generated patch failed git apply --check: %w: %s", err, out)
	}
	fd, err := resolveAt(int(store.dir.Fd()), PatchFileName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), PatchFileName)
	_, err = f.Write(patch)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = store.dir.Sync()
	}
	if err != nil {
		unix.Unlinkat(int(store.dir.Fd()), PatchFileName, 0)
		return nil, err
	}
	return paths, nil
}

func patchGitEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			env = append(env, e)
		}
	}
	return append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
}
