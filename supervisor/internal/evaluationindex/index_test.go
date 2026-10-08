package evaluationindex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLedgerDocumentValidatesWithoutRuntimeDependencies(t *testing.T) {
	path := filepath.Join("..", "..", "..", "docs", "evaluation-evidence-index-2026-10-07.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ValidateJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Entries) == 0 || doc.Entries[0].Fulfilled {
		t.Fatalf("unexpected ledger state: %+v", doc.Entries[0])
	}
	for _, entry := range doc.Entries {
		if entry.Fulfilled {
			t.Fatalf("fulfilled entry in current ledger: %q", entry.ID)
		}
	}
}

func TestRejectsFulfilledOrInflatedClaim(t *testing.T) {
	b := validDocument(t)
	b = strings.Replace(b, `"status":"blocked"`, `"status":"fulfilled"`, 1)
	b = strings.Replace(b, `"fulfilled":false`, `"fulfilled":true`, 1)
	if _, err := ValidateJSON([]byte(b)); err == nil {
		t.Fatal("fulfilled ledger entry was accepted")
	}
}

func TestRejectsUnmatchedAcceptedCommit(t *testing.T) {
	b := validDocument(t)
	b = strings.Replace(b, `"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`, 1)
	if _, err := ValidateJSON([]byte(b)); err == nil {
		t.Fatal("unmatched accepted commit was accepted")
	}
}

func TestStrictJSONPrivateProbes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name: "duplicate key",
			mutate: func(b []byte) []byte {
				return bytes.Replace(b, []byte(`"fulfilled":false`), []byte(`"fulfilled":false,"fulfilled":false`), 1)
			},
		},
		{
			name: "case variant key",
			mutate: func(b []byte) []byte {
				return bytes.Replace(b, []byte(`"fulfilled":false`), []byte(`"Fulfilled":false`), 1)
			},
		},
		{
			name: "invalid UTF-8",
			mutate: func(b []byte) []byte {
				at := bytes.Index(b, []byte("Fixture"))
				b[at] = 0xff
				return b
			},
		},
		{
			name: "unexpected null",
			mutate: func(b []byte) []byte {
				return bytes.Replace(b, []byte(`"fulfilled":false`), []byte(`"fulfilled":null`), 1)
			},
		},
		{
			name:   "trailing JSON",
			mutate: func(b []byte) []byte { return append(b, []byte(` {}`)...) }},
		{
			name: "unknown exact field",
			mutate: func(b []byte) []byte {
				return bytes.Replace(b, []byte(`"notes":"Fixture"`), []byte(`"extra":"x","notes":"Fixture"`), 1)
			},
		},
		{
			name: "missing required field",
			mutate: func(b []byte) []byte {
				return bytes.Replace(b, []byte(`"fulfilled":false,`), nil, 1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ValidateJSON(test.mutate([]byte(validDocument(t)))); err == nil {
				t.Fatal("private JSON probe was accepted")
			}
		})
	}
	if _, err := ValidateJSON([]byte(strings.Replace(validDocument(t), `"fulfilled":false`, `"fulfilled":true`, 1))); err == nil {
		t.Fatal("fulfilled=true was accepted")
	}
}

func TestStrictJSONNestedPaths(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name: "nested null array",
			mutate: func(b []byte) []byte {
				return bytes.Replace(b, []byte(`"source_citations":[`), []byte(`"source_citations":null,"unused":[`), 1)
			},
		},
		{
			name: "nested case key",
			mutate: func(b []byte) []byte {
				return bytes.Replace(b, []byte(`"review_scope":"source-only"`), []byte(`"Review_scope":"source-only"`), 1)
			},
		},
		{
			name: "nested duplicate key",
			mutate: func(b []byte) []byte {
				return bytes.Replace(b, []byte(`"scope":"docs"`), []byte(`"scope":"docs","scope":"docs"`), 1)
			},
		},
		{
			name: "nested unknown key",
			mutate: func(b []byte) []byte {
				return bytes.Replace(b, []byte(`"scope":"docs"`), []byte(`"unknown":"x","scope":"docs"`), 1)
			},
		},
		{
			name: "nested null boolean",
			mutate: func(b []byte) []byte {
				return bytes.Replace(b, []byte(`"host_observation_revalidated":false`), []byte(`"host_observation_revalidated":null`), 1)
			},
		},
		{
			name: "nested null object",
			mutate: func(b []byte) []byte {
				return bytes.Replace(b, []byte(`"expected_counts":{"cases":0}`), []byte(`"expected_counts":null`), 1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ValidateJSON(test.mutate([]byte(validDocument(t)))); err == nil {
				t.Fatal("nested strict JSON probe was accepted")
			}
		})
	}
}

func TestRejectsMissingNullOrEmptyEntryBounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(Document)
	}{
		{name: "null entries", mutate: func(doc Document) { doc.Entries = nil }},
		{name: "empty entries", mutate: func(doc Document) { doc.Entries = []LedgerItem{} }},
		{name: "empty status vocabulary", mutate: func(doc Document) { doc.StatusVocabulary = []string{} }},
		{name: "empty source citations", mutate: func(doc Document) { doc.Entries[0].SourceCitations = []Citation{} }},
		{name: "empty required artifacts", mutate: func(doc Document) { doc.Entries[0].RequiredArtifacts = []string{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc := minimalDocument()
			test.mutate(doc)
			b, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateJSON(b); err == nil {
				t.Fatal("empty or missing bound was accepted")
			}
		})
	}
	if _, err := ValidateJSON(withoutRootField(t, validDocument(t), "entries")); err == nil {
		t.Fatal("omitted entries bound was accepted")
	}
}

func TestRejectsUnsafeCitationTraversal(t *testing.T) {
	b := validDocument(t)
	b = strings.Replace(b, `"ref":"repo:docs/source.md"`, `"ref":"repo:../source.md"`, 1)
	if _, err := ValidateJSON([]byte(b)); err == nil {
		t.Fatal("unsafe citation reference was accepted")
	}
}

func TestRejectsUnsafePathsAndReversedLines(t *testing.T) {
	unsafeRefs := []string{
		"repo:C:/Windows/system32",
		"repo://server/share",
		"repo:../secret",
		"repo:\\secret",
		"https://example.invalid/source",
		"repo:docs/\x00secret",
	}
	for _, ref := range unsafeRefs {
		t.Run("ref-"+ref, func(t *testing.T) {
			doc := minimalDocument()
			doc.Entries[0].SourceCitations[0].Ref = ref
			b, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateJSON(b); err == nil {
				t.Fatalf("unsafe reference %q was accepted", ref)
			}
		})
	}
	for _, artifact := range []string{"C:/Windows/system32", `\\server\share`, "https://example.invalid", "../secret"} {
		t.Run("artifact-"+artifact, func(t *testing.T) {
			doc := minimalDocument()
			doc.Entries[0].RequiredArtifacts[0] = artifact
			b, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateJSON(b); err == nil {
				t.Fatalf("unsafe artifact %q was accepted", artifact)
			}
		})
	}
	for _, date := range []string{"not-a-date", "2026-02-30"} {
		t.Run("date-"+date, func(t *testing.T) {
			doc := minimalDocument()
			doc.Entries[0].SourceCitations[0].SourceDate = date
			b, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateJSON(b); err == nil {
				t.Fatalf("invalid date %q was accepted", date)
			}
		})
	}
	doc := minimalDocument()
	doc.Entries[0].SourceCitations[0].Lines = "999-1"
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateJSON(b); err == nil {
		t.Fatal("reversed line range was accepted")
	}
}

func TestRejectsUnregisteredRecordedSourceAndArithmeticDrift(t *testing.T) {
	doc := minimalDocument()
	doc.Entries[0].EvidenceRefs = []EvidenceRef{{Kind: "recorded-source", ID: "", Ref: "repo:docs/not-registered.md", Scope: "external", Status: "external-recorded-source-only"}}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateJSON(b); err == nil {
		t.Fatal("unregistered recorded source was accepted")
	}

	doc = minimalDocument()
	doc.Entries[0].ExpectedCounts = map[string]int{"left": 2, "right": 3, "result": 5}
	doc.Entries[0].ArithmeticConstraints = []ArithmeticConstraint{{Name: "sum", LeftKey: "left", Operator: "+", RightKey: "right", ResultKey: "result", ExpectedResult: 6, Status: "declared"}}
	b, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateJSON(b); err == nil {
		t.Fatal("arithmetic drift was accepted")
	}
}

func TestRejectsDuplicateExternalSourceIdentity(t *testing.T) {
	doc := minimalDocument()
	doc.Provenance.ExternalRecordedSources = append(doc.Provenance.ExternalRecordedSources, doc.Provenance.ExternalRecordedSources[0])
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateJSON(b); err == nil {
		t.Fatal("duplicate external source identity was accepted")
	}
}

func TestRejectsCitationCountDrift(t *testing.T) {
	doc := minimalDocument()
	doc.Provenance.CitationCount = 2
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateJSON(b); err == nil {
		t.Fatal("citation count drift was accepted")
	}
}

func TestValidateArithmeticRejectsIntOverflowBeforeComparison(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	minInt := -maxInt - 1

	tests := []struct {
		name   string
		left   int
		right  int
		op     string
		result int
	}{
		{
			name:   "addition overflow with wrapped negative result",
			left:   maxInt,
			right:  1,
			op:     "+",
			result: minInt,
		},
		{
			name:   "multiplication overflow with matching positive wrap",
			left:   maxInt,
			right:  3,
			op:     "*",
			result: maxInt - 2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := LedgerItem{
				ExpectedCounts: map[string]int{
					"left": test.left, "right": test.right, "result": test.result,
				},
				ArithmeticConstraints: []ArithmeticConstraint{{
					Name: "overflow", LeftKey: "left", Operator: test.op,
					RightKey: "right", ResultKey: "result", ExpectedResult: test.result, Status: "declared",
				}},
			}
			if err := validateArithmetic(entry); err == nil {
				t.Fatal("overflowing arithmetic was accepted by wrapped comparison")
			}
		})
	}
}

func TestValidateArithmeticAcceptsSafeIntegerBoundaries(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	if _, ok := checkedNonnegativeAdd(maxInt, 1); ok {
		t.Fatal("addition helper accepted int overflow")
	}
	if _, ok := checkedNonnegativeMultiply(maxInt, 3); ok {
		t.Fatal("multiplication helper accepted int overflow")
	}
	tests := []struct {
		name     string
		left     int
		right    int
		operator string
		result   int
	}{
		{name: "addition at max via zero", left: maxInt, right: 0, operator: "+", result: maxInt},
		{name: "multiplication at max via one", left: maxInt, right: 1, operator: "*", result: maxInt},
		{name: "multiplication by zero", left: maxInt, right: 0, operator: "*", result: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := LedgerItem{
				ExpectedCounts: map[string]int{"left": test.left, "right": test.right, "result": test.result},
				ArithmeticConstraints: []ArithmeticConstraint{{
					Name: "safe-boundary", LeftKey: "left", Operator: test.operator,
					RightKey: "right", ResultKey: "result", ExpectedResult: test.result, Status: "declared",
				}},
			}
			if err := validateArithmetic(entry); err != nil {
				t.Fatalf("safe boundary rejected: %v", err)
			}
		})
	}
}

func TestRejectsCollectionAndStringBounds(t *testing.T) {
	doc := minimalDocument()
	doc.Entries = make([]LedgerItem, MaxEntries+1)
	for i := range doc.Entries {
		doc.Entries[i] = minimalDocument().Entries[0]
		doc.Entries[i].ID = "entry-" + strings.Repeat("x", 3) + string(rune('a'+i%26))
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateJSON(b); err == nil {
		t.Fatal("oversized entries array was accepted")
	}

	doc = minimalDocument()
	doc.Entries[0].RequiredArtifacts = make([]string, MaxRequiredArtifacts+1)
	for i := range doc.Entries[0].RequiredArtifacts {
		doc.Entries[0].RequiredArtifacts[i] = "artifact"
	}
	b, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateJSON(b); err == nil {
		t.Fatal("oversized required-artifacts array was accepted")
	}

	doc = minimalDocument()
	doc.Entries[0].Title = strings.Repeat("x", MaxStringBytes)
	b, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateJSON(b); err == nil {
		t.Fatal("oversized string was accepted")
	}
}

func TestValidateJSONDirectSizeBound(t *testing.T) {
	if _, err := ValidateJSON(bytes.Repeat([]byte{' '}, MaxIndexSize+1)); err == nil {
		t.Fatal("oversized direct input was accepted")
	}
}

func TestRejectsMissingPinpointCitation(t *testing.T) {
	doc := minimalDocument()
	doc.Entries[0].SourceCitations = nil
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateJSON(b); err == nil {
		t.Fatal("entry without source citation was accepted")
	}
}

func TestValidateReaderIsBounded(t *testing.T) {
	if _, err := ValidateReader(strings.NewReader(strings.Repeat("x", MaxIndexSize+1))); err == nil {
		t.Fatal("oversized reader was accepted")
	}
}

func validDocument(t *testing.T) string {
	t.Helper()
	doc := minimalDocument()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func minimalDocument() Document {
	return Document{
		Schema:           Schema,
		IndexVersion:     1,
		ReviewDate:       "2026-10-07",
		AttestationScope: "structural-only",
		Provenance: Provenance{
			ReviewScope:              "source-only",
			CurrentCheckout:          SourceIdentity{Commit: "1111111111111111111111111111111111111111", CommitDate: "2026-10-07", Scope: "docs"},
			AcceptedComponentCommits: []AcceptedCommit{{Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Scope: "fixture", Status: "accepted-component"}},
			CitationCount:            1,
			ExternalRecordedSources: []ExternalSource{{
				Ref: "repo:docs/external.md", BaseCommit: "2222222222222222222222222222222222222222", BaseBlob: "3333333333333333333333333333333333333333", WorkingTreeBlob: "3333333333333333333333333333333333333333", SHA256: "4444444444444444444444444444444444444444444444444444444444444444", Status: "external-recorded-source-only", HostObservationRevalidated: false,
			}},
		},
		StatusVocabulary: []string{"blocked", "partial-component", "not-evidenced", "component-accepted"},
		Entries: []LedgerItem{{
			ID: "fixture", Kind: "study", Title: "Fixture", ClaimScope: "none", Status: "blocked", Fulfilled: false,
			SourceCitations:   []Citation{{Ref: "repo:docs/source.md", SourceDate: "2026-10-07", Locator: "section", Lines: "1-2"}},
			RequiredArtifacts: []string{"fixture artifact"}, ExpectedCounts: map[string]int{"cases": 0}, ArithmeticConstraints: []ArithmeticConstraint{},
			EvidenceRefs: []EvidenceRef{{Kind: "accepted-commit", ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Ref: "repo:docs/source.md", Scope: "fixture", Status: "component-only"}},
		}},
	}
}

func withoutRootField(t *testing.T, source, field string) []byte {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(source), &object); err != nil {
		t.Fatal(err)
	}
	delete(object, field)
	b, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
