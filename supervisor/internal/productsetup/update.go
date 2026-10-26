package productsetup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// copyAtomic copies src to dst through a synced temporary file in dst's directory and a rename, so dst
// is always either its old or its new content.
func copyAtomic(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tbound-copy-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(mode); err == nil {
		if _, err = io.Copy(tmp, in); err == nil {
			err = tmp.Sync()
		}
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func fileDigest(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func regularFile(file string) (os.FileInfo, error) {
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() {
		return nil, refuse("%s is not a regular non-symlink file", file)
	}
	return info, nil
}

// Update installs newBinary as the service binary. See replaceBinary for the flow.
func Update(env Env, newBinary string) (Report, error) {
	h := newHost(env)
	if err := h.openRoot(); err != nil {
		return *h.rep, err
	}
	defer h.dir.Close()
	info, err := regularFile(newBinary)
	if err != nil {
		return *h.rep, err
	}
	if info.Size() == 0 || info.Size() > maxBinaryBytes {
		return *h.rep, refuse("%s is empty or too large to be the tbound binary", newBinary)
	}
	return h.replaceBinary(newBinary, "update")
}

// Rollback restores the last-known-good binary the same way Update installs a new one. The binary it
// replaces becomes the new last-known-good, so a second rollback returns to it.
func Rollback(env Env) (Report, error) {
	h := newHost(env)
	if err := h.openRoot(); err != nil {
		return *h.rep, err
	}
	defer h.dir.Close()
	return h.replaceBinary(h.real(LastKnownGoodPath), "rollback")
}

// replaceBinary swaps source in as the service binary:
//  1. stage a copy next to the binary;
//  2. stop the service and open the catalog: any turn that is not awaiting review refuses the swap and
//     the service starts again, whose restart reconciliation settles the turn;
//  3. back the catalog up, keep the current binary as last-known-good, and rename the staged copy over
//     the binary, each step atomic;
//  4. start the service and run the self-test. A failed self-test restores the previous binary.
//
// An interruption leaves the binary either old or new, and the last-known-good beside it. Rerunning the
// command converges, and the service is started again when it was left stopped.
func (h *host) replaceBinary(source, what string) (Report, error) {
	current, staged := h.real(BinaryPath), h.real(stagedBinaryPath)
	if _, err := regularFile(current); err != nil {
		return *h.rep, refuse("no tbound binary is installed at %s; run `tbound product setup` first", BinaryPath)
	}
	if _, err := regularFile(source); err != nil {
		if what == "rollback" {
			return *h.rep, refuse("there is no last-known-good binary at %s to roll back to", LastKnownGoodPath)
		}
		return *h.rep, err
	}
	want, err := fileDigest(source)
	if err != nil {
		return *h.rep, err
	}
	if have, err := fileDigest(current); err != nil {
		return *h.rep, err
	} else if have == want {
		h.step("binary", BinaryPath, "unchanged", "already sha256:"+want[:16])
		return *h.rep, h.startAndTest("kept from the interrupted update")
	}
	if h.env.DryRun {
		h.step("stage", stagedBinaryPath, "created", "sha256:"+want[:16])
		_ = h.command("systemctl", "stop", UnitName)
		h.step("catalog", CatalogPath, "unchanged", "would open after the stop and refuse on an unresolved turn")
		h.step("swap", BinaryPath, "updated", "current binary kept as "+LastKnownGoodPath)
		_ = h.command("systemctl", "start", UnitName)
		h.rep.Commands = append(h.rep.Commands, BinaryPath+" product doctor --self-test")
		return *h.rep, nil
	}
	if err := h.copyAtomic(source, stagedBinaryPath, 0o755); err != nil {
		return *h.rep, fmt.Errorf("stage the binary: %w", err)
	}
	h.step("stage", stagedBinaryPath, "created", "sha256:"+want[:16])
	if err := h.command("systemctl", "stop", UnitName); err != nil {
		_, _ = h.remove(stagedBinaryPath, false)
		return *h.rep, err
	}
	abort := func(err error) error {
		_, _ = h.remove(stagedBinaryPath, false)
		return errors.Join(err, h.command("systemctl", "start", UnitName))
	}
	if h.env.Turns == nil {
		return *h.rep, abort(errors.New("no catalog check configured"))
	}
	turns, err := h.env.Turns()
	if err != nil {
		return *h.rep, abort(fmt.Errorf("the catalog cannot be opened by this binary (a newer schema is never read by an older one): %w", err))
	}
	var unresolved []string
	pending := 0
	for _, t := range turns {
		if t.State == StateAwaitingReview {
			pending++
		} else {
			unresolved = append(unresolved, t.ID+" ("+t.State+")")
		}
	}
	if len(unresolved) > 0 {
		return *h.rep, abort(refuse("unresolved turns: %s. The service was started again so its restart reconciliation can settle them; retry the %s once they are terminal",
			strings.Join(unresolved, ", "), what))
	}
	if pending > 0 {
		h.note("%d pending review(s) will be discarded with reason restart when the service starts", pending)
	}
	backup, err := h.backupCatalog()
	if err != nil {
		return *h.rep, abort(fmt.Errorf("back up the catalog: %w", err))
	}
	if err := h.copyAtomic(current, LastKnownGoodPath, 0o755); err != nil {
		return *h.rep, abort(fmt.Errorf("keep the last-known-good binary: %w", err))
	}
	if err := h.chown(LastKnownGoodPath, ownRoot); err != nil {
		return *h.rep, abort(err)
	}
	if err := h.withPath(BinaryPath, false, func(path string) error {
		return os.Rename(filepath.Join(filepath.Dir(path), filepath.Base(staged)), path)
	}); err != nil {
		return *h.rep, abort(fmt.Errorf("swap the binary: %w", err))
	}
	h.step("swap", BinaryPath, "updated", "sha256:"+want[:16]+"; previous binary kept as "+LastKnownGoodPath)
	return *h.rep, h.startAndTest(backup)
}

func (h *host) startAndTest(backup string) error {
	if err := h.command("systemctl", "start", UnitName); err != nil {
		return h.restore(err, backup)
	}
	if h.env.DryRun {
		h.rep.Commands = append(h.rep.Commands, BinaryPath+" product doctor --self-test")
		return nil
	}
	switch err := h.selfTest(); {
	case err == nil:
		h.step("self-test", BinaryPath, "created", "passed")
	case errors.Is(err, ErrSelfTestNotConfigured):
		h.note("no self-test ran (not configured, or not run on this root): no runtime turn exercised the new binary, so governed readiness is not established")
	default:
		return h.restore(fmt.Errorf("%w: %v", ErrSelfTest, err), backup)
	}
	return nil
}

// selfTest runs the hook, or records it where nothing can run (a test root).
func (h *host) selfTest() error {
	if h.env.SelfTestHook == nil {
		h.rep.Commands = append(h.rep.Commands, BinaryPath+" product doctor --self-test")
		return ErrSelfTestNotConfigured
	}
	h.rep.Commands = append(h.rep.Commands, BinaryPath+" product doctor --self-test")
	return h.env.SelfTestHook()
}

// restore puts the last-known-good binary back after a failed start or self-test, then starts it.
func (h *host) restore(cause error, backup string) error {
	err := errors.Join(cause, h.command("systemctl", "stop", UnitName),
		h.copyAtomic(h.real(LastKnownGoodPath), BinaryPath, 0o755), h.command("systemctl", "start", UnitName))
	h.step("swap", BinaryPath, "updated", "rolled back to "+LastKnownGoodPath)
	return fmt.Errorf("%w; rolled back to the last-known-good binary (catalog backup: %s)", err, backup)
}

// backupCatalog copies the catalog journal to BackupsDir under a name that carries its digest, and
// returns the logical backup path, or "none" when no catalog exists yet.
func (h *host) backupCatalog() (string, error) {
	catalog := h.real(CatalogPath)
	if _, err := os.Lstat(catalog); errors.Is(err, os.ErrNotExist) {
		return "none", nil
	}
	sum, err := fileDigest(catalog)
	if err != nil {
		return "", err
	}
	logical := BackupsDir + "/catalog-" + sum[:16] + ".jsonl"
	if err := h.copyAtomic(catalog, logical, 0o600); err != nil {
		return "", err
	}
	h.step("backup", logical, "created", "catalog journal before the swap")
	return logical, h.chown(logical, ownService)
}

// Uninstall stops and disables the service and removes the unit, the binary and the runtime access. It
// reports the catalog's unresolved turns, and keeps ConfigDir and StateDir (the registration, the
// credentials, the catalog, the turn evidence and the projects) unless purge is set. A purge asks
// Env.Confirm before it changes anything.
func Uninstall(env Env, purge bool) (Report, error) {
	h := newHost(env)
	if err := h.openRoot(); err != nil {
		return *h.rep, err
	}
	defer h.dir.Close()
	if purge && !env.DryRun {
		ok, err := false, error(nil)
		if env.Confirm != nil {
			ok, err = env.Confirm(fmt.Sprintf("--purge permanently deletes %s (registration, provider credentials) and %s (catalog, turn evidence, projects). Confirm", ConfigDir, StateDir))
		}
		if err != nil || !ok {
			return *h.rep, refuse("the purge was not confirmed (use --yes to confirm without a prompt); nothing was changed")
		}
	}
	for _, args := range [][]string{{"stop", UnitName}, {"disable", UnitName}} {
		commandErr := h.command("systemctl", args...)
		if env.Live && !env.DryRun {
			absent, err := h.inactiveUnit()
			if err != nil {
				return *h.rep, errors.Join(commandErr, err)
			}
			if commandErr != nil && !absent {
				return *h.rep, commandErr
			}
		} else if commandErr != nil {
			return *h.rep, commandErr
		}
	}
	if purge && !env.DryRun && env.Turns == nil {
		return *h.rep, errors.New("no catalog check configured; refusing purge")
	}
	if env.Turns != nil && !env.DryRun {
		if turns, err := env.Turns(); err != nil {
			if purge {
				return *h.rep, fmt.Errorf("the catalog could not be read; refusing purge: %w", err)
			}
			h.note("the catalog could not be read: %v", err)
		} else if len(turns) > 0 {
			ids := make([]string, len(turns))
			for i, t := range turns {
				ids[i] = t.ID + " (" + t.State + ")"
			}
			h.note("%d unresolved turn(s) stay in the catalog: %s. Start the service again to reconcile them before relying on the evidence", len(turns), strings.Join(ids, ", "))
		}
	}
	unitGone, err := h.remove(UnitPath, false)
	if err != nil {
		return *h.rep, err
	}
	for _, p := range []string{BinaryPath, LastKnownGoodPath, stagedBinaryPath} {
		if _, err := h.remove(p, false); err != nil {
			return *h.rep, err
		}
	}
	if err := h.removeEmptyDir(BinaryDir); err != nil {
		return *h.rep, err
	}
	if _, err := h.remove(RuntimeDir, true); err != nil {
		return *h.rep, err
	}
	if unitGone {
		if err := h.command("systemctl", "daemon-reload"); err != nil {
			return *h.rep, err
		}
	}
	for _, dir := range []string{ConfigDir, StateDir} {
		if purge {
			if _, err := h.remove(dir, true); err != nil {
				return *h.rep, err
			}
		} else {
			h.step("keep", dir, "unchanged", "kept; use --purge to delete")
		}
	}
	h.note("the %s account and group are kept; remove them with userdel and groupdel if no file needs them", ServiceUser)
	return *h.rep, nil
}

// remove deletes a path below the root and reports whether it existed.
func (h *host) remove(logical string, all bool) (bool, error) {
	err := h.withPath(logical, false, func(path string) error {
		if _, err := os.Lstat(path); err != nil {
			return err
		}
		if !h.writes() {
			return nil
		}
		if all {
			return os.RemoveAll(path)
		}
		return os.Remove(path)
	})
	if errors.Is(err, os.ErrNotExist) {
		h.step("remove", logical, "unchanged", "absent")
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("remove %s: %w", logical, err)
	}
	h.step("remove", logical, "removed", "")
	return true, nil
}

func (h *host) removeEmptyDir(logical string) error {
	if h.writes() {
		err := h.withPath(logical, false, os.Remove)
		if err != nil && !errors.Is(err, os.ErrNotExist) && !directoryNotEmpty(err) {
			return err
		}
	}
	return nil
}

func (h *host) copyAtomic(source, logical string, mode os.FileMode) error {
	return h.withPath(logical, true, func(path string) error { return copyAtomic(source, path, mode) })
}

// inactiveUnit requires positive systemd state, rather than inferring absence from a failed command.
func (h *host) inactiveUnit() (bool, error) {
	h.rep.Commands = append(h.rep.Commands, "systemctl show "+UnitName+" --property=LoadState --property=ActiveState")
	out, err := h.exec("systemctl", "show", UnitName, "--property=LoadState", "--property=ActiveState")
	if err != nil {
		return false, fmt.Errorf("cannot verify the service is inactive: %w", err)
	}
	var load, active string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if value, ok := strings.CutPrefix(line, "LoadState="); ok {
			load = value
		}
		if value, ok := strings.CutPrefix(line, "ActiveState="); ok {
			active = value
		}
	}
	if (load != "loaded" && load != "not-found") || active != "inactive" {
		return false, refuse("service inactivity was not confirmed (LoadState=%s, ActiveState=%s); nothing was removed", load, active)
	}
	return load == "not-found", nil
}
