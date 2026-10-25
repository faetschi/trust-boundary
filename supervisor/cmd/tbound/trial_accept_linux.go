//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/taskaccept"
)

// `tbound trial accept <case_id> [--tree live]` is the guest half of the task oracle
// (TASK-COMPLETE-01). It evaluates the acceptance artifact that a compliant case pins
// (acceptance_test.digest, installed next to the fixture artifacts) against the post-run
// live root, with the fixture of the case as the baseline for unchanged_except, and prints
// one tbound-accept-result/v1 document. It refuses as any uid other than root, like the other
// trial verbs, and changes nothing.
//
// The case, its fixture and its acceptance artifact come from the pinned corpus manifest and
// the root-owned artifact directory, never from the caller, and each artifact must recompute
// to the digest the manifest pins. The tree is read through taskaccept.RootFS: the live root is
// opened without following a link, every lookup is one openat2 that refuses symbolic links and
// leaving the root, and a link in the tree is a non-regular entry at its own path. A case with
// no acceptance artifact (an ambiguous case is scored against its rubric, an adversarial case
// by its oracles) is refused with an error and no document.
//
// The only tree is the live root. --tree is accepted for the runner's explicitness and must say
// "live". The document also names its route (taskaccept.RouteTrialAcceptCell) and the digest of the live
// tree it judged (trialfix.TreeDigest), which is what ties a retained result to the fixture or to the
// fixture with the reference solution applied.

// trialCase is what the task verbs take from the pinned corpus manifest for one case.
type trialCase struct {
	ID   string
	Kind evalcorpus.Kind
	// FixtureDigest is the workspace_fixture_digest, AcceptDigest the acceptance_test digest
	// of a compliant case (empty otherwise).
	FixtureDigest  string
	ManifestDigest string
	AcceptDigest   string
	// Prompt and DurationSeconds are the host-approved prompt and duration of the case's trial.
	Prompt          string
	DurationSeconds uint64
}

// trialTaskEnv is what `accept` and `v-run` touch outside the fields of trialEnv they share
// (the artifacts, the live root and the fixture owner); tests replace it.
type trialTaskEnv struct {
	// pinnedCase resolves a case of the pinned corpus manifest.
	pinnedCase   func(ctx context.Context, caseID string) (trialCase, error)
	binaryDigest func() (string, error)

	// The rest belongs to v-run.
	template func(ctx context.Context) (vTemplate, error)
	// preflight checks what podman needs on the guest before a work directory is made.
	preflight func() error
	podman    podmanRunner
	// workRoot holds one private directory per run; runtimeBundle is the Pi runtime bundle
	// and keyPath the provider key file, both mounted read-only into the container.
	workRoot, runtimeBundle, keyPath string
	now                              func() time.Time
	// token returns a random token that names a run's work directory and container.
	token func() (string, error)
	// deadline turns a case duration in seconds into the hard deadline of the container run
	// (a function so a test can shorten it), and killGrace is how long the host waits beyond it
	// for podman's own timeout before it kills podman.
	deadline  func(seconds uint64) time.Duration
	killGrace time.Duration
}

func productionTrialTaskEnv() trialTaskEnv {
	return trialTaskEnv{
		pinnedCase: trialPinnedCase, binaryDigest: trialBinaryDigest,
		template: loadVTemplate, preflight: vPreflight, podman: productionPodmanRunner(),
		workRoot: vWorkRoot, runtimeBundle: piruntime.PiCLIProductionRuntimeBundleRoot, keyPath: piruntime.D09ManagedPiProviderCredentialPath,
		now: time.Now, token: randomVToken,
		deadline:  func(s uint64) time.Duration { return time.Duration(s) * time.Second },
		killGrace: vKillGrace,
	}
}

// trialPinnedCase is the case of the pinned corpus manifest: the manifest the signed service
// config references, admitted with the service's own loader and read again to find the case.
func trialPinnedCase(ctx context.Context, caseID string) (trialCase, error) {
	registration, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return trialCase{}, err
	}
	_, contract, err := serviceSocketRegistrationFromSignedRegistration(registration)
	if err != nil {
		return trialCase{}, err
	}
	config, err := readProductionPiServiceConfig(contract.ServiceConfigPath, contract.ServiceConfigDigest)
	if err != nil {
		return trialCase{}, err
	}
	corpus, err := loadProductionCorpus(config)
	if err != nil {
		return trialCase{}, err
	}
	data, err := readCorpusManifest()
	if err != nil {
		return trialCase{}, err
	}
	manifest, err := evalcorpus.Load(bytes.NewReader(data))
	if err != nil {
		return trialCase{}, err
	}
	if digest, err := evalcorpus.Digest(manifest); err != nil || digest != corpus.digest {
		return trialCase{}, errors.New("the corpus manifest changed while it was read")
	}
	return pinnedCaseOf(manifest, caseID)
}

// pinnedCaseOf extracts the task facts of one case of an admitted manifest.
func pinnedCaseOf(manifest evalcorpus.Manifest, caseID string) (trialCase, error) {
	for _, c := range manifest.Cases {
		if c.CaseID != caseID {
			continue
		}
		fixture, ok := corpusadmit.FixtureDigest(c)
		if !ok {
			return trialCase{}, errors.New("the case has no workspace fixture digest")
		}
		digest, err := evalcorpus.Digest(manifest)
		if err != nil {
			return trialCase{}, err
		}
		out := trialCase{ID: caseID, Kind: c.Kind, FixtureDigest: fixture, ManifestDigest: digest}
		if c.Kind == evalcorpus.KindCompliant {
			out.AcceptDigest = c.Compliant.AcceptanceTest.Digest
		}
		if c.Trial != nil {
			out.Prompt, out.DurationSeconds = c.Trial.Prompt, c.Trial.DurationSeconds
		}
		return out, nil
	}
	return trialCase{}, errors.New("not a case of the pinned corpus manifest")
}

// checkTrialCaseID accepts the safe alphabet of a case id, as `reset` does.
func checkTrialCaseID(caseID string) error {
	if len(caseID) == 0 || len(caseID) > evidenceMaxValueBytes || strings.HasPrefix(caseID, "-") || !evidenceWordRE.MatchString(caseID) {
		return errors.New("the case id is not in the safe alphabet")
	}
	return nil
}

// parseTrialAccept accepts "<case_id>" or "<case_id> --tree live".
func parseTrialAccept(args []string) (string, error) {
	if len(args) != 1 && len(args) != 3 {
		return "", errors.New("accept takes a case id and optionally --tree live")
	}
	if err := checkTrialCaseID(args[0]); err != nil {
		return "", err
	}
	if len(args) == 3 {
		if args[1] != "--tree" {
			return "", fmt.Errorf("accept: unknown argument %q", args[1])
		}
		if args[2] != "live" {
			return "", fmt.Errorf("accept: --tree must be %q, not %q", "live", args[2])
		}
	}
	return args[0], nil
}

func trialBinaryDigest() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func trialAccept(ctx context.Context, args []string, env trialEnv) (taskaccept.AcceptDoc, error) {
	// Like guest-evidence --commit, this is a capture label checked against the frozen run by import.
	commit := ""
	if len(args) >= 3 && args[len(args)-2] == "--commit" {
		commit = args[len(args)-1]
		if !guestevidence.IsCommit(commit) {
			return taskaccept.AcceptDoc{}, errors.New("accept: --commit must be 40 lowercase hex digits")
		}
		args = args[:len(args)-2]
	}
	if len(args) == 2 && args[1] == "--tree" {
		return taskaccept.AcceptDoc{}, errors.New("accept: --tree needs a value")
	}
	caseID, err := parseTrialAccept(args)
	if err != nil {
		return taskaccept.AcceptDoc{}, err
	}
	c, err := env.task.pinnedCase(ctx, caseID)
	if err != nil {
		return taskaccept.AcceptDoc{}, fmt.Errorf("case %q: %w", caseID, err)
	}
	switch c.Kind {
	case evalcorpus.KindCompliant:
	case evalcorpus.KindAmbiguous:
		return taskaccept.AcceptDoc{}, fmt.Errorf("case %q is ambiguous: it has no acceptance artifact and is scored against its rubric", caseID)
	default:
		return taskaccept.AcceptDoc{}, fmt.Errorf("case %q (%s) has no acceptance artifact", caseID, c.Kind)
	}
	fixture, err := corpusadmit.LoadFixture(env.artifacts, c.FixtureDigest)
	if err != nil {
		return taskaccept.AcceptDoc{}, fmt.Errorf("case %q: %w", caseID, err)
	}
	spec, err := corpusadmit.LoadAccept(env.artifacts, c.AcceptDigest)
	if err != nil {
		return taskaccept.AcceptDoc{}, fmt.Errorf("case %q: %w", caseID, err)
	}
	device, inode, err := env.registeredRoot(ctx)
	if err != nil {
		return taskaccept.AcceptDoc{}, fmt.Errorf("the managed live root registration cannot be verified: %w", err)
	}
	root, err := taskaccept.OpenRootPinned(env.liveRoot, taskaccept.RootIdentity{Device: device, Inode: inode})
	if err != nil {
		return taskaccept.AcceptDoc{}, err
	}
	defer root.Close()
	// The result is bound to the tree it judged: the digest before and after must agree, so a tree that
	// changes while it is evaluated yields no document.
	before, err := env.liveTreeDigest(device, inode)
	if err != nil {
		return taskaccept.AcceptDoc{}, err
	}
	binary, err := env.task.binaryDigest()
	if err != nil {
		return taskaccept.AcceptDoc{}, err
	}
	tmpl, err := env.task.template(ctx)
	if err != nil {
		return taskaccept.AcceptDoc{}, err
	}
	// Resolve once: every cell check uses exactly the image named in the result.
	env.task.template = func(context.Context) (vTemplate, error) { return tmpl, nil }
	doc, err := taskaccept.EvaluateCapturedRootWith(caseID, spec, fixture, root, cellNodeRunner{ctx: ctx, task: env.task})
	if err != nil {
		return taskaccept.AcceptDoc{}, err
	}
	after, err := env.liveTreeDigest(device, inode)
	if err != nil {
		return taskaccept.AcceptDoc{}, err
	}
	if after != before {
		return taskaccept.AcceptDoc{}, errors.New("the live tree changed while it was evaluated")
	}
	doc.Route = taskaccept.RouteTrialAcceptCell
	doc.Commit, doc.BinarySHA256, doc.ImageDigest, doc.ManifestDigest = commit, binary, tmpl.ImageDigest, c.ManifestDigest
	return doc, nil
}

// runTrialTask runs one of the task verbs and writes its document: one JSON line on stdout.
func runTrialTask(ctx context.Context, verb string, args []string, stdout io.Writer, env trialEnv) error {
	var doc any
	var err error
	switch verb {
	case "accept":
		doc, err = trialAccept(ctx, args, env)
	case "v-run":
		doc, err = trialVRun(ctx, args, env)
	default:
		return fmt.Errorf("unknown trial verb %q", verb)
	}
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(encoded, '\n'))
	return err
}
