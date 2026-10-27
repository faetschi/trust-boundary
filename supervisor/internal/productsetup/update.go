package productsetup

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"tbound/supervisor/internal/releasetrust"
)

// copyAtomic copies in to dst through a synced temporary file in dst's directory and a rename, so dst
// is always either its old or its new content.
func copyAtomic(in io.ReadSeeker, dst string, mode os.FileMode) error {
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return err
	}
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

func fileDigest(f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func regularFile(file *os.File) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, refuse("%s is not a regular file", file.Name())
	}
	return info, nil
}

func (h *host) managedSource(logical string, o owner, mode os.FileMode) (*os.File, error) {
	f, err := h.openFile(logical, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	info, err := regularFile(f)
	if err == nil && o == ownService && h.env.Live && !h.haveIDs {
		h.uid, h.gid, h.haveIDs, err = h.lookupUser(ServiceUser)
	}
	if err == nil && (!h.ownerMatches(info, o) || (mode == 0 && modeBits(info)&^0o040 != 0o600) || (mode != 0 && modeBits(info) != mode)) {
		err = refuse("%s has an unexpected owner or mode", logical)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// interruptHook lets a test end the process at a step boundary of an update or rollback, to prove a
// fresh process recovers. It is nil in production.
var interruptHook func(step string)

func interrupt(step string) {
	if interruptHook != nil {
		interruptHook(step)
	}
}

// Candidate is the binary an update installs and the signed release manifest that must list it.
type Candidate struct{ Binary, Manifest, Signature string }

// Update installs the candidate as the service binary. See replaceBinary for the flow.
//
// The candidate is authenticated before anything else happens, because the self-test runs the installed
// binary as root. The signed manifest must verify under the publisher key that setup pinned, and must
// list the candidate by name with its size and SHA-256. The digest is taken from a copy that only root can
// write, so a file swapped after the check changes nothing, and only that copy is installed or executed. A
// dry run measures the candidate in place and installs and executes nothing. Without a pinned key an
// update is refused.
func Update(env Env, candidate Candidate) (Report, error) {
	h := newHost(env)
	if err := h.openRoot(); err != nil {
		return *h.rep, err
	}
	defer h.dir.Close()
	if err := h.protectedBinaryDirectory(); err != nil {
		return *h.rep, err
	}
	manifest, err := h.publisherManifest(candidate)
	if err != nil {
		return *h.rep, err
	}
	source, err := openSource(candidate.Binary)
	if err != nil {
		return *h.rep, err
	}
	defer source.Close()
	info, err := regularFile(source)
	if err != nil {
		return *h.rep, err
	}
	if info.Size() == 0 || info.Size() > maxBinaryBytes {
		return *h.rep, refuse("%s is empty or too large to be the tbound binary", candidate.Binary)
	}
	listed, err := manifest.Artifact(filepath.Base(candidate.Binary))
	if err != nil {
		return *h.rep, refuse("the candidate is refused: %v", err)
	}
	verified := source
	if !env.DryRun {
		defer h.discardStaged()
		// One byte past the declared size is enough for the size comparison to refuse a source that grew.
		if err := h.copyAtomic(io.NewSectionReader(source, 0, listed.Size+1), stagedBinaryPath, 0o755); err != nil {
			return *h.rep, fmt.Errorf("stage the binary: %w", err)
		}
		interrupt("staged")
		if verified, err = h.managedSource(stagedBinaryPath, ownRoot, 0o755); err != nil {
			return *h.rep, err
		}
		defer verified.Close()
	}
	if err := h.authenticate(manifest, filepath.Base(candidate.Binary), verified); err != nil {
		return *h.rep, err
	}
	interrupt("verified")
	if err := h.checkBinaryFormat(verified, candidate.Binary); err != nil {
		return *h.rep, err
	}
	if err := h.checkInstalledSelfTestClient(); err != nil {
		return *h.rep, err
	}
	return h.replaceBinary(verified)
}

// protectedBinaryDirectory refuses an update or rollback below a binary directory that another user could write
// to, because the staged copy and the last-known-good binary are checked by descriptor and then swapped in by name.
func (h *host) protectedBinaryDirectory() error {
	err := h.protectedDirectory(BinaryDir)
	if errors.Is(err, os.ErrNotExist) {
		return refuse("no safe tbound binary is installed at %s; run `tbound product setup` first: %v", BinaryPath, err)
	}
	return err
}

// publisherManifest verifies the candidate's manifest and signature under the pinned publisher key.
func (h *host) publisherManifest(candidate Candidate) (releasetrust.Manifest, error) {
	var keyFile *os.File
	err := h.protectedDirectory(filepath.Dir(ReleasePublicKeyPath))
	if err == nil {
		keyFile, err = h.managedSource(ReleasePublicKeyPath, ownRoot, 0o644)
	}
	if errors.Is(err, os.ErrNotExist) {
		return releasetrust.Manifest{}, refuse("no publisher key is pinned (%s is missing), so no candidate binary can be authenticated; set release_public_key in the setup config and rerun `tbound product setup --replace`", ReleasePublicKeyPath)
	} else if err != nil {
		return releasetrust.Manifest{}, refuse("the pinned publisher key %s cannot be used: %v", ReleasePublicKeyPath, err)
	}
	defer keyFile.Close()
	content, err := io.ReadAll(io.LimitReader(keyFile, 1<<10))
	if err != nil {
		return releasetrust.Manifest{}, err
	}
	key, err := releasetrust.ParsePublicKey(content)
	if err != nil {
		return releasetrust.Manifest{}, refuse("the pinned publisher key %s is malformed: %v", ReleasePublicKeyPath, err)
	}
	manifest, err := readSource(candidate.Manifest)
	if err != nil {
		return releasetrust.Manifest{}, refuse("cannot read the release manifest: %v", err)
	}
	signatureText, err := readBounded(candidate.Signature, 1<<10)
	if err != nil {
		return releasetrust.Manifest{}, refuse("cannot read the manifest signature: %v", err)
	}
	signature, err := releasetrust.ParseSignature(signatureText)
	if err != nil {
		return releasetrust.Manifest{}, refuse("%v", err)
	}
	verified, err := releasetrust.Verify(manifest, signature, key)
	if err != nil {
		return releasetrust.Manifest{}, refuse("the release manifest is not signed by the pinned publisher key %s: %v", releasetrust.Fingerprint(key), err)
	}
	h.note("the release manifest verifies under the pinned publisher key %s", releasetrust.Fingerprint(key))
	return verified, nil
}

// authenticate refuses a binary that the signed manifest does not list under its name with this size and
// SHA-256. It measures the file it is given, which for a live run is the private staged copy.
func (h *host) authenticate(manifest releasetrust.Manifest, name string, binary *os.File) error {
	info, err := binary.Stat()
	if err != nil {
		return err
	}
	digest, err := fileDigest(binary)
	if err != nil {
		return err
	}
	if err := manifest.CheckArtifact(name, info.Size(), digest); err != nil {
		return refuse("the candidate is refused: %v", err)
	}
	h.note("%s (sha256:%s) is listed in the verified manifest", name, digest[:16])
	return nil
}

// discardStaged removes the staged copy, if any, and does not report it.
func (h *host) discardStaged() { _ = h.withPath(stagedBinaryPath, false, os.Remove) }

// replaceBinary swaps source, the authenticated staged copy (a dry run's candidate in place), in as the service
// binary:
//  1. the copy was staged next to the binary by Update;
//  2. stop the service and open the catalog: any turn that is not awaiting review refuses the swap and
//     the service starts again, whose restart reconciliation settles the turn;
//  3. back the catalog up, keep the current binary as last-known-good, and rename the staged copy over
//     the binary, each step atomic;
//  4. start the service and run the self-test. A failed self-test restores the previous binary.
//
// An interruption leaves the binary either old or new, and the last-known-good beside it. Rerunning the
// command converges, and the service is started again when it was left stopped.
func (h *host) replaceBinary(source *os.File) (Report, error) {
	if err := h.finishRestoration(); err != nil {
		return *h.rep, err
	}
	current, err := h.managedSource(BinaryPath, ownRoot, 0o755)
	if err != nil {
		return *h.rep, refuse("no safe tbound binary is installed at %s; run `tbound product setup` first: %v", BinaryPath, err)
	}
	defer current.Close()
	want, err := fileDigest(source)
	if err != nil {
		return *h.rep, err
	}
	if have, err := fileDigest(current); err != nil {
		return *h.rep, err
	} else if have == want {
		fallback, err := h.managedSource(LastKnownGoodPath, ownRoot, 0o755)
		if err == nil {
			fallback.Close()
		} else if !errors.Is(err, os.ErrNotExist) {
			return *h.rep, err
		}
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
	h.step("stage", stagedBinaryPath, "created", "sha256:"+want[:16])
	abort, err := h.stopForSwap("update", func() { _, _ = h.remove(stagedBinaryPath, false) })
	if err != nil {
		return *h.rep, err
	}
	interrupt("stopped")
	backup, err := h.backupCatalog()
	if err != nil {
		return *h.rep, abort(fmt.Errorf("back up the catalog: %w", err))
	}
	interrupt("backed-up")
	if err := h.copyAtomic(current, LastKnownGoodPath, 0o755); err != nil {
		return *h.rep, abort(fmt.Errorf("keep the last-known-good binary: %w", err))
	}
	if err := h.chown(LastKnownGoodPath, ownRoot); err != nil {
		return *h.rep, abort(err)
	}
	interrupt("prepared")
	verified, err := source.Stat()
	if err != nil {
		return *h.rep, abort(err)
	}
	if err := h.withPath(BinaryPath, false, func(path string) error {
		staged := filepath.Join(filepath.Dir(path), filepath.Base(stagedBinaryPath))
		if info, err := os.Lstat(staged); err != nil || !os.SameFile(info, verified) {
			return refuse("%s was replaced after it was authenticated; nothing was installed", stagedBinaryPath)
		}
		return os.Rename(staged, path)
	}); err != nil {
		return *h.rep, abort(fmt.Errorf("swap the binary: %w", err))
	}
	h.step("swap", BinaryPath, "updated", "sha256:"+want[:16]+"; previous binary kept as "+LastKnownGoodPath)
	interrupt("swapped")
	return *h.rep, h.startAndTest(backup)
}

// stopForSwap stops the service and opens the catalog. A turn that is not awaiting review refuses the
// swap, because the service must start again to reconcile it. A refusal runs discard and starts the
// service again, which the returned abort does for any later failure. On success the service stays
// stopped.
func (h *host) stopForSwap(what string, discard func()) (abort func(error) error, err error) {
	if err := h.command("systemctl", "stop", UnitName); err != nil {
		discard()
		return nil, err
	}
	abort = func(err error) error {
		discard()
		return errors.Join(err, h.command("systemctl", "start", UnitName))
	}
	if h.env.Turns == nil {
		return nil, abort(errors.New("no catalog check configured"))
	}
	turns, err := h.env.Turns()
	if err != nil {
		return nil, abort(fmt.Errorf("the catalog cannot be opened by this binary (a newer schema is never read by an older one): %w", err))
	}
	var unresolved []string
	pending := 0
	for _, t := range turns {
		switch t.State {
		case StateAwaitingReview:
			pending++
		case StateUnknown: // no start settles it, and a swap leaves the registration alone
		default:
			unresolved = append(unresolved, t.ID+" ("+t.State+")")
		}
	}
	if len(unresolved) > 0 {
		return nil, abort(refuse("unresolved turns: %s. The service was started again so its restart reconciliation can settle them; retry the %s once they are terminal",
			strings.Join(unresolved, ", "), what))
	}
	if pending > 0 {
		h.note("%d pending review(s) will be discarded with reason restart when the service starts", pending)
	}
	return abort, nil
}

// Rollback restores the last-known-good binary. The binary it replaces becomes the new last-known-good,
// so a second rollback returns to it. An interrupted rollback is finished by running it again. Rollback
// takes no candidate: it only ever runs the last-known-good binary, a root-owned file that is the binary an
// earlier authenticated update (or the setup run by the administrator) installed, so it needs no
// signature of its own.
func Rollback(env Env) (Report, error) {
	h := newHost(env)
	if err := h.openRoot(); err != nil {
		return *h.rep, err
	}
	defer h.dir.Close()
	if err := h.protectedBinaryDirectory(); err != nil {
		return *h.rep, err
	}
	if fallback, err := h.managedSource(LastKnownGoodPath, ownRoot, 0o755); err == nil { // a missing one is refused by rollback
		err = h.checkBinaryFormat(fallback, LastKnownGoodPath)
		fallback.Close()
		if err != nil {
			return *h.rep, err
		}
	}
	if err := h.checkInstalledSelfTestClient(); err != nil {
		return *h.rep, err
	}
	return h.rollback()
}

// checkBinaryFormat refuses, before anything changes, a candidate binary of another installation format than
// the installed unit and setup record. A binary reads only its own format (a new doctor rejects the old
// unit, an old binary rejects the new [Install] section and client_uid), so crossing would fail its
// self-test and restore, and nothing converts one format into the other.
func (h *host) checkBinaryFormat(candidate io.ReadSeeker, what string) error {
	if h.env.BinaryFormat == nil {
		return nil
	}
	const way = "run `tbound product setup --replace` with a tbound binary of format %d, which rewrites the unit and the setup record"
	data, err := readSource(h.real(SetupConfigPath))
	if err != nil {
		return refuse("the installed setup record is unreadable, so the installation format cannot be checked: %v", err)
	}
	record, err := parseInstalled(data)
	if err != nil {
		return refuse("the installed setup record cannot be read by this binary, so the installation format cannot be checked (%v); "+way, err, InstallationFormat)
	}
	installed := cmp.Or(record.InstallationFormat, LegacyInstallationFormat)
	got, err := h.env.BinaryFormat(candidate)
	if err != nil {
		return refuse("cannot tell the installation format of %s: %v", what, err)
	}
	if got != installed {
		return refuse("%s is installation format %d but this installation is format %d; nothing was changed. The formats do not convert: "+way, what, got, installed, got)
	}
	return nil
}

// checkSetupBinaryFormat refuses a --binary of another installation format than the one this setup writes
// (its unit and setup record), so setup never pairs a binary with files it cannot read.
func (h *host) checkSetupBinaryFormat(binary []byte, what string) error {
	if h.env.BinaryFormat == nil {
		return nil
	}
	got, err := h.env.BinaryFormat(bytes.NewReader(binary))
	if err != nil {
		return refuse("cannot tell the installation format of %s: %v", what, err)
	}
	if got != InstallationFormat {
		return refuse("%s is installation format %d but this setup writes format %d; nothing was changed. Run `tbound product setup` from the tbound binary to install, or pass a --binary of format %d",
			what, got, InstallationFormat, InstallationFormat)
	}
	return nil
}

// rollback exchanges the installed binary and the last-known-good binary with one RENAME_EXCHANGE, so
// both exist whichever way an interruption falls and neither is overwritten. Before the exchange it
// records the digests of both in rollbackIntentPath. A retry in a fresh process reads them. An installed
// binary that already is the target means the exchange happened, so the retry only starts the service
// and runs the self-test, and never exchanges back. The old pair still in place means it carries on.
// Any other pair means the record is stale, and it is dropped. The record is removed once the restored
// binary started and passed, or was restored.
func (h *host) rollback() (Report, error) {
	if err := h.finishRestoration(); err != nil {
		return *h.rep, err
	}
	current, err := h.managedSource(BinaryPath, ownRoot, 0o755)
	if err != nil {
		return *h.rep, refuse("no safe tbound binary is installed at %s; run `tbound product setup` first: %v", BinaryPath, err)
	}
	defer current.Close()
	fallback, err := h.managedSource(LastKnownGoodPath, ownRoot, 0o755)
	if err != nil {
		return *h.rep, refuse("there is no last-known-good binary at %s safe to roll back to: %v", LastKnownGoodPath, err)
	}
	defer fallback.Close()
	installed, err := fileDigest(current)
	if err != nil {
		return *h.rep, err
	}
	kept, err := fileDigest(fallback)
	if err != nil {
		return *h.rep, err
	}
	installed, kept = "sha256:"+installed, "sha256:"+kept
	record, found, err := h.readRollbackIntent()
	if err != nil {
		return *h.rep, err
	}
	switch {
	case found && installed == record.Target:
		h.note("resuming the interrupted rollback: %s is already installed", installed[:23])
		return *h.rep, h.startAndTest("kept from the interrupted rollback")
	case found && (installed != record.Previous || kept != record.Target):
		if err := h.dropRollbackIntent(); err != nil {
			return *h.rep, err
		}
	}
	if installed == kept {
		return *h.rep, refuse("the last-known-good binary is the installed binary (%s); there is nothing to roll back to", installed[:23])
	}
	detail := "exchanged with " + LastKnownGoodPath + ": " + kept[:23] + " installed, " + installed[:23] + " kept as last-known-good"
	if h.env.DryRun {
		_ = h.command("systemctl", "stop", UnitName)
		h.step("catalog", CatalogPath, "unchanged", "would open after the stop and refuse on an unresolved turn")
		h.step("swap", BinaryPath, "updated", detail)
		_ = h.command("systemctl", "start", UnitName)
		h.rep.Commands = append(h.rep.Commands, BinaryPath+" product doctor --self-test")
		return *h.rep, nil
	}
	abort, err := h.stopForSwap("rollback", func() {})
	if err != nil {
		return *h.rep, err
	}
	interrupt("stopped")
	backup, err := h.backupCatalog()
	if err != nil {
		return *h.rep, abort(fmt.Errorf("back up the catalog: %w", err))
	}
	interrupt("backed-up")
	if err := h.writeRollbackIntent(rollbackIntent{Target: kept, Previous: installed}); err != nil {
		return *h.rep, abort(errors.Join(fmt.Errorf("record the rollback: %w", err), h.dropRollbackIntent()))
	}
	interrupt("prepared")
	if err := h.exchange(BinaryPath, LastKnownGoodPath); err != nil {
		return *h.rep, abort(errors.Join(fmt.Errorf("exchange the binaries: %w", err), h.dropRollbackIntent()))
	}
	h.step("swap", BinaryPath, "updated", detail)
	interrupt("swapped")
	return *h.rep, h.startAndTest(backup)
}

// rollbackIntent is the persisted state of a rollback or restoration in progress. A rollback records the
// digests (sha256:HEX) of the binary being restored and of the binary being rolled back from. A restoration
// (after a failed start or self-test) records only Target, the last-known-good binary being put back.
type rollbackIntent struct {
	Target    string `json:"target"`
	Previous  string `json:"previous,omitempty"`
	Restoring bool   `json:"restoring,omitempty"`
}

func (h *host) readRollbackIntent() (rollbackIntent, bool, error) {
	f, err := h.managedSource(rollbackIntentPath, ownRoot, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return rollbackIntent{}, false, nil
	}
	if err != nil {
		return rollbackIntent{}, false, refuse("the rollback record %s is unsafe (%v); check which binary is installed, then remove it", rollbackIntentPath, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<10))
	var record rollbackIntent
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err != nil || decoder.Decode(&record) != nil || !sha256Digest.MatchString(record.Target) || (!record.Restoring && !sha256Digest.MatchString(record.Previous)) {
		return rollbackIntent{}, false, refuse("the rollback record %s is unreadable; check which binary is installed, then remove it", rollbackIntentPath)
	}
	return record, true, nil
}

// writeRollbackIntent makes the record durable before the exchange.
func (h *host) writeRollbackIntent(record rollbackIntent) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := h.withPath(rollbackIntentPath, true, func(path string) error { return writeFile(path, append(data, '\n'), 0o600) }); err != nil {
		return err
	}
	if err := h.chown(rollbackIntentPath, ownRoot); err != nil {
		return err
	}
	return h.syncDir(BinaryDir)
}

// dropRollbackIntent removes the record, if any, and makes the removal durable.
func (h *host) dropRollbackIntent() error {
	if !h.writes() {
		return nil
	}
	err := h.withPath(rollbackIntentPath, false, os.Remove)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove the rollback record: %w", err)
	}
	return h.syncDir(BinaryDir)
}

func (h *host) startAndTest(backup string) error {
	if err := h.command("systemctl", "start", UnitName); err != nil {
		return h.restore(err, backup)
	}
	interrupt("started")
	if h.env.DryRun {
		h.rep.Commands = append(h.rep.Commands, BinaryPath+" product doctor --self-test")
		return nil
	}
	switch err := h.selfTest(); {
	case err == nil:
		interrupt("tested")
		h.step("self-test", BinaryPath, "created", "passed")
	case errors.Is(err, ErrSelfTestNotConfigured):
		h.note("no self-test ran (not configured, or not run on this root): no runtime turn exercised the new binary, so governed readiness is not established")
	default:
		return h.restore(fmt.Errorf("%w: %v", ErrSelfTest, err), backup)
	}
	return h.dropRollbackIntent()
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
	source, err := h.managedSource(LastKnownGoodPath, ownRoot, 0o755)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("cannot restore the last-known-good binary: %w", err))
	}
	defer source.Close()
	digest, err := fileDigest(source)
	if err != nil {
		return errors.Join(cause, err)
	}
	// The restoration is recorded first. A process that ends below leaves both binaries equal and the service
	// stopped, and the next update or rollback finishes it (finishRestoration) before it compares anything.
	// Without the record nothing below may run, so the new binary stays installed and `rollback` is the way out.
	if err := h.writeRollbackIntent(rollbackIntent{Target: "sha256:" + digest, Restoring: true}); err != nil {
		return errors.Join(cause, fmt.Errorf("the restoration of %s could not be recorded, so nothing was stopped or replaced and the new binary is still installed; run `tbound product rollback`: %w", LastKnownGoodPath, err))
	}
	failed := errors.Join(h.command("systemctl", "stop", UnitName), h.copyAtomic(source, BinaryPath, 0o755))
	interrupt("restored")
	failed = errors.Join(failed, h.command("systemctl", "start", UnitName))
	if failed == nil {
		failed = h.dropRollbackIntent()
	}
	h.step("swap", BinaryPath, "updated", "rolled back to "+LastKnownGoodPath)
	return fmt.Errorf("%w; rolled back to the last-known-good binary (catalog backup: %s)", errors.Join(cause, failed), backup)
}

// finishRestoration finishes a restoration that an interrupted update or rollback recorded: it puts the
// last-known-good binary back when the copy had not completed, starts the service, runs the self-test and
// clears the record. Without such a record it does nothing. A restoration leaves the installed binary equal
// to the last-known-good one, so an update or rollback must call this before it compares the two.
func (h *host) finishRestoration() error {
	record, found, err := h.readRollbackIntent()
	if err != nil || !found || !record.Restoring {
		return err
	}
	if h.env.DryRun {
		return refuse("an interrupted restoration of %s is recorded in %s; a dry run cannot plan past it: rerun without --dry-run to finish it", record.Target[:23], rollbackIntentPath)
	}
	fallback, err := h.managedSource(LastKnownGoodPath, ownRoot, 0o755)
	if err != nil {
		return refuse("an interrupted restoration is recorded but the last-known-good binary is unusable: %v", err)
	}
	defer fallback.Close()
	kept, err := fileDigest(fallback)
	if err != nil {
		return err
	}
	if "sha256:"+kept != record.Target {
		return refuse("the restoration record names %s but the last-known-good binary is sha256:%s; check which binary is installed, then remove %s", record.Target, kept, rollbackIntentPath)
	}
	h.note("finishing the interrupted restoration of %s first", record.Target[:23])
	current, err := h.managedSource(BinaryPath, ownRoot, 0o755)
	if err != nil {
		return refuse("no safe tbound binary is installed at %s; run `tbound product setup` first: %v", BinaryPath, err)
	}
	defer current.Close()
	installed, err := fileDigest(current)
	if err != nil {
		return err
	}
	if "sha256:"+installed != record.Target {
		if err := errors.Join(h.command("systemctl", "stop", UnitName), h.copyAtomic(fallback, BinaryPath, 0o755)); err != nil {
			return fmt.Errorf("finish the restoration: %w", err)
		}
	}
	return h.startAndTest("kept from the interrupted restoration")
}

// backupCatalog copies the catalog journal to BackupsDir under a name that carries its digest, and
// returns the logical backup path, or "none" when no catalog exists yet.
func (h *host) backupCatalog() (string, error) {
	catalog, err := h.managedSource(CatalogPath, ownService, 0)
	if errors.Is(err, os.ErrNotExist) {
		return "none", nil
	}
	if err != nil {
		return "", err
	}
	defer catalog.Close()
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
		} else {
			var ids, blocked []string
			for _, t := range turns {
				if name := t.ID + " (" + t.State + ")"; t.State == StateUnknown {
					blocked = append(blocked, name)
				} else {
					ids = append(ids, name)
				}
			}
			if len(ids) > 0 {
				h.note("%d unresolved turn(s) stay in the catalog: %s. Start the service again to reconcile them before relying on the evidence", len(ids), strings.Join(ids, ", "))
			}
			if len(blocked) > 0 {
				h.note("%d turn(s) with an unknown outcome stay in the catalog and stay blocked: %s", len(blocked), strings.Join(blocked, ", "))
			}
		}
	}
	unitGone, err := h.remove(UnitPath, false)
	if err != nil {
		return *h.rep, err
	}
	for _, p := range []string{BinaryPath, LastKnownGoodPath, stagedBinaryPath, rollbackIntentPath} {
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
	if !env.Live { // `systemctl disable` removed the link on a live host; a test root only recorded it
		if _, err := h.remove(enableLinkPath, false); err != nil {
			return *h.rep, err
		}
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

func (h *host) copyAtomic(source io.ReadSeeker, logical string, mode os.FileMode) error {
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
