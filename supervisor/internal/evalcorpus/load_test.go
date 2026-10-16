package evalcorpus

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// validManifestJSON returns the JSON of an allocated synthetic manifest.
func validManifestJSON(t *testing.T) string {
	t.Helper()
	q := smallQuotas()
	raw, err := json.Marshal(allocated(t, synthManifest(q, synthSeed), q))
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(raw)
}

// replaceOnce replaces the first occurrence of old in s and fails if old is absent.
func replaceOnce(t *testing.T, s, old, repl string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("fixture lacks %q", old)
	}
	return strings.Replace(s, old, repl, 1)
}

// wantLoadError asserts that Load rejects raw with an error containing substr.
func wantLoadError(t *testing.T, raw, substr string) {
	t.Helper()
	_, err := Load(strings.NewReader(raw))
	if err == nil {
		t.Fatalf("Load accepted the input; want an error containing %q", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("Load error = %q; want it to contain %q", err, substr)
	}
}

func TestLoadRoundTripKeepsDigest(t *testing.T) {
	q := smallQuotas()
	orig := allocated(t, synthManifest(q, synthSeed), q)
	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Load of a valid manifest: %v", err)
	}
	want, err := Digest(orig)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	gotDigest, err := Digest(got)
	if err != nil {
		t.Fatalf("Digest after Load: %v", err)
	}
	if gotDigest != want {
		t.Errorf("digest after Load = %s; want %s", gotDigest, want)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	base := validManifestJSON(t)
	t.Run("top level", func(t *testing.T) {
		wantLoadError(t, replaceOnce(t, base, `{"schema_version"`, `{"surprise":1,"schema_version"`), `unknown field "surprise"`)
	})
	t.Run("nested adaptation record", func(t *testing.T) {
		wantLoadError(t, replaceOnce(t, base, `"preserved_intent":"synthetic intent"`, `"preserved_intent":"synthetic intent","extra_field":"x"`), `unknown field "extra_field"`)
	})
}

func TestLoadRejectsCaseVariantKeys(t *testing.T) {
	base := validManifestJSON(t)
	// encoding/json matches keys case-insensitively; the exact-key check must still reject.
	wantLoadError(t, replaceOnce(t, base, `"case_id":`, `"Case_Id":`), `unknown field "Case_Id"`)
}

func TestLoadRejectsDuplicateFields(t *testing.T) {
	base := validManifestJSON(t)
	t.Run("top level", func(t *testing.T) {
		wantLoadError(t, replaceOnce(t, base, `"split_seed":"synthetic-seed-1"`, `"split_seed":"synthetic-seed-1","split_seed":"other"`), `duplicate field "split_seed"`)
	})
	t.Run("nested", func(t *testing.T) {
		wantLoadError(t, replaceOnce(t, base, `"tool_mapping":"synthetic tool mapping"`, `"tool_mapping":"synthetic tool mapping","tool_mapping":"other"`), `duplicate field "tool_mapping"`)
	})
}

func TestLoadRejectsMissingRequiredField(t *testing.T) {
	base := validManifestJSON(t)
	wantLoadError(t, replaceOnce(t, base, `"split_seed":"synthetic-seed-1",`, ``), `missing field "split_seed"`)
}

func TestLoadRejectsNull(t *testing.T) {
	base := validManifestJSON(t)
	wantLoadError(t, replaceOnce(t, base, `"split_seed":"synthetic-seed-1"`, `"split_seed":null`), "null is not permitted")
}

func TestLoadRejectsWrongType(t *testing.T) {
	base := validManifestJSON(t)
	wantLoadError(t, replaceOnce(t, base, `"split_seed":"synthetic-seed-1"`, `"split_seed":5`), "split_seed")
}

func TestLoadRejectsTrailingData(t *testing.T) {
	wantLoadError(t, validManifestJSON(t)+" {}", "trailing data")
}

func TestLoadRejectsInvalidUTF8(t *testing.T) {
	base := validManifestJSON(t)
	wantLoadError(t, replaceOnce(t, base, "synthetic intent", "synthetic \xff intent"), "UTF-8")
}

// Lone surrogate escapes decode to U+FFFD, so two manifests that differ only in
// such an escape would share a digest. Load must reject them.
func TestLoadRejectsLoneSurrogateEscapes(t *testing.T) {
	base := validManifestJSON(t)
	for _, tt := range []struct{ name, esc string }{
		{"lone high", `\uD800`},
		{"lone low", `\uDC00`},
		{"high followed by a non-surrogate", `\uD800A`},
		{"low followed by high", `\uDC00\uD800`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wantLoadError(t, replaceOnce(t, base, "synthetic intent", "synthetic "+tt.esc+" intent"), "unpaired surrogate")
		})
	}
}

func TestLoadAcceptsPairedSurrogatesAndEscapedBackslash(t *testing.T) {
	base := validManifestJSON(t)
	for _, esc := range []string{`😀`, `\\uD800`} {
		if _, err := Load(strings.NewReader(replaceOnce(t, base, "synthetic intent", "synthetic "+esc+" intent"))); err != nil {
			t.Errorf("Load with %s: %v", esc, err)
		}
	}
}

func TestLoadRejectsNonObjectTopLevel(t *testing.T) {
	wantLoadError(t, `[]`, "expected an object")
}

func TestLoadRejectsOversizedInput(t *testing.T) {
	_, err := Load(bytes.NewReader(make([]byte, MaxManifestBytes+1)))
	if err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("Load oversized input: err = %v; want a size error", err)
	}
}
