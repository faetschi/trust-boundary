//go:build linux

package productsetup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
)

// contentDigestRegistration is a registration "verifier" whose digest follows the content, so a changed
// registration has another digest. The text after the first line is a revision note.
func contentDigestRegistration(reg, _, _ []byte) (RegistrationSummary, error) {
	list, ok := strings.CutPrefix(string(reg), "projects: ")
	if !ok {
		return RegistrationSummary{}, errors.New("bad registration")
	}
	list, _, _ = strings.Cut(list, "\n")
	sum := sha256.Sum256(reg)
	return RegistrationSummary{ID: "reg", Digest: "sha256:" + hex.EncodeToString(sum[:]), Projects: strings.Split(list, ","), Profiles: []string{"opencode-go-engineering"}}, nil
}

func TestSetupReplaceChecksTheCatalogOnlyWhenTheRegistrationDigestChanges(t *testing.T) {
	f := newFixture(t)
	f.env.VerifyRegistration = contentDigestRegistration
	f.installed()
	f.env.Replace = true
	calls := 0
	turns := []OpenTurn{{ID: "psess-1/turn-1", State: "linked"}, {ID: "psess-2/turn-3", State: StateUnknown}}
	f.env.Turns = func() ([]OpenTurn, error) { calls++; return turns, nil }

	if _, err := f.setup(); err != nil || calls != 0 {
		t.Fatalf("without a catalog nothing can be unsettled: %v (checked %d times)", err, calls)
	}
	if err := os.WriteFile(f.path(CatalogPath), []byte("catalog journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.setup(); err != nil || calls != 0 {
		t.Fatalf("the same registration keeps the digest, so it is not fenced: %v (checked %d times)", err, calls)
	}

	installed := f.read(RegistrationPath)
	if err := os.WriteFile(f.cfg.Registration, []byte("projects: alpha,beta\nrevision 2"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dryRun := range []bool{false, true} {
		f.env.DryRun = dryRun
		rep, err := f.setup()
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "psess-1/turn-1 (linked)") || !strings.Contains(err.Error(), "psess-2/turn-3 (unknown)") ||
			!strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("dry run %v: the replacement was not refused naming both turns: %v", dryRun, err)
		}
		if f.read(RegistrationPath) != installed || len(rep.Commands) != 0 {
			t.Fatalf("dry run %v: a refused replacement changed the host: %v", dryRun, rep.Commands)
		}
	}

	f.env.DryRun = false
	f.env.Turns = func() ([]OpenTurn, error) { return nil, errors.New("journal is locked") }
	if _, err := f.setup(); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "journal is locked") || f.read(RegistrationPath) != installed {
		t.Fatalf("a catalog that cannot be opened must refuse the replacement: %v", err)
	}
	f.env.Turns = nil
	if _, err := f.setup(); !errors.Is(err, ErrRefused) || f.read(RegistrationPath) != installed {
		t.Fatalf("a missing catalog check must refuse the replacement: %v", err)
	}

	f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
	rep, err := f.setup()
	if err != nil || f.read(RegistrationPath) == installed || !strings.Contains(rep.String(), "no unsettled turn under the installed registration") {
		t.Fatalf("a replacement with no unsettled turn: %v\n%s", err, rep)
	}
}

func TestUpdateIgnoresATurnWithAnUnknownOutcomeButNamesTheOthers(t *testing.T) {
	f := newFixture(t)
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) { return []OpenTurn{{ID: "psess-2/turn-3", State: StateUnknown}}, nil }
	if rep, err := Update(f.env, f.newBinary("binary v2")); err != nil || f.read(BinaryPath) != "binary v2" {
		t.Fatalf("an unknown outcome does not change what an update does: %v\n%s", err, rep)
	}
	f.env.Turns = func() ([]OpenTurn, error) {
		return []OpenTurn{{ID: "psess-2/turn-3", State: StateUnknown}, {ID: "psess-1/turn-1", State: "linked"}}, nil
	}
	_, err := Update(f.env, f.newBinary("binary v3"))
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "psess-1/turn-1 (linked)") || strings.Contains(err.Error(), "turn-3") {
		t.Fatalf("an unresolved turn must still refuse, and only it: %v", err)
	}
}
