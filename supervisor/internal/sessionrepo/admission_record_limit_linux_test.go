//go:build linux

package sessionrepo

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/workspace"
)

// A workspace whose smallest admission record cannot fit one audit record is refused before any
// effect: the command never runs, nothing is copied, and the Store stays usable. Long paths make the
// manifest large with few files, so the fixture seeds quickly.
func TestMutationRefusesOversizeAdmissionRecordBeforeEffect(t *testing.T) {
	store, _, _, authority := newE2EStore(t)
	source := filepath.Join(t.TempDir(), "source")
	writeFixtureDir(t, source, 0o700)
	deep, relative := source, ""
	for i := 0; i < 4; i++ {
		segment := strings.Repeat(string(rune('a'+i)), 250)
		deep, relative = filepath.Join(deep, segment), filepath.Join(relative, segment)
		writeFixtureDir(t, deep, 0o755)
	}
	for i := 0; i < 2600; i++ {
		writeFixtureFile(t, filepath.Join(deep, fmt.Sprintf("f%05d", i)), "x", 0o644)
	}
	g0, err := store.Seed(openFixtureRoot(t, source), RootAttestation{Quiescent: true})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	ran := false
	_, err = store.RunBash(context.Background(), g0, lease("oversize"), allow("oversize"),
		CommandSpec{Executable: "/bin/bash", Args: []string{"-c", "true"}, DeclaredOutputPaths: []string{"out.txt"}},
		CommandRunnerFunc(func(context.Context, *CommandView, CommandSpec) (CommandResult, CommandSettlement, error) {
			ran = true
			return CommandResult{}, CommandSettlement{}, errors.New("runner must not be called")
		}))
	if !errors.Is(err, ErrAdmissionRecordLimit) || !errors.Is(err, workspace.ErrLimit) || ran {
		t.Fatalf("bash on oversize workspace: err=%v ran=%v, want ErrAdmissionRecordLimit before the runner", err, ran)
	}
	if _, err := store.Write(context.Background(), g0, proposal("oversize-write"), allow("oversize-write"), "out.txt", []byte("x")); !errors.Is(err, ErrAdmissionRecordLimit) {
		t.Fatalf("write on oversize workspace: %v, want ErrAdmissionRecordLimit", err)
	}

	path := filepath.ToSlash(filepath.Join(relative, "f00000"))
	readOp, readDecision := proposal("after-refusal"), allow("after-refusal")
	authority.grant(OperationRequest{Tool: "read", Operation: readOp, Decision: readDecision, InputGeneration: g0.ID(), InputTreeDigest: g0.TreeDigest(), ArgumentDigest: readArgumentsDigest(path, 16)})
	if got, err := g0.Read(context.Background(), readOp, readDecision, path, 16); err != nil || string(got) != "x" {
		t.Fatalf("store unusable after refusal: bytes=%q err=%v", got, err)
	}
}
