// Package evaluationindex validates the small, source-only evaluation ledger.
// It accepts bytes or an io.Reader; it never opens paths or performs runtime
// infrastructure, provider, credential, or host operations.
package evaluationindex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Schema                   = "tbound/evaluation-evidence-index/v1"
	MaxIndexSize             = 1 << 20
	MaxStringBytes           = 64 << 10
	MaxObjectFields          = 128
	MaxExpectedCountFields   = 128
	MaxStatusValues          = 32
	MaxEntries               = 256
	MaxAcceptedCommits       = 128
	MaxExternalSources       = 128
	MaxCitationsPerEntry     = 128
	MaxRequiredArtifacts     = 256
	MaxEvidenceRefs          = 128
	MaxArithmeticConstraints = 128
	MaxDependencies          = 256
)

var (
	hex40     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	lineRange = regexp.MustCompile(`^[0-9]+(-[0-9]+)?(;[0-9]+(-[0-9]+)?)*$`)
)

type Document struct {
	Schema           string       `json:"$schema"`
	IndexVersion     int          `json:"index_version"`
	ReviewDate       string       `json:"review_date"`
	AttestationScope string       `json:"attestation_scope"`
	Provenance       Provenance   `json:"provenance"`
	StatusVocabulary []string     `json:"status_vocabulary"`
	Entries          []LedgerItem `json:"entries"`
}

type Provenance struct {
	ReviewScope              string           `json:"review_scope"`
	CurrentCheckout          SourceIdentity   `json:"current_checkout"`
	AcceptedComponentCommits []AcceptedCommit `json:"accepted_component_commits"`
	ExternalRecordedSources  []ExternalSource `json:"external_recorded_sources"`
	CitationCount            int              `json:"citation_count"`
}

type SourceIdentity struct {
	Commit     string `json:"commit"`
	CommitDate string `json:"commit_date"`
	Scope      string `json:"scope"`
}

type AcceptedCommit struct {
	Commit string `json:"commit"`
	Scope  string `json:"scope"`
	Status string `json:"status"`
}

type ExternalSource struct {
	Ref                        string `json:"ref"`
	BaseCommit                 string `json:"base_commit"`
	BaseBlob                   string `json:"base_blob"`
	WorkingTreeBlob            string `json:"working_tree_blob"`
	SHA256                     string `json:"sha256"`
	Status                     string `json:"status"`
	HostObservationRevalidated bool   `json:"host_observation_revalidated"`
}

type Citation struct {
	Ref        string `json:"ref"`
	SourceDate string `json:"source_date"`
	Locator    string `json:"locator"`
	Lines      string `json:"lines"`
}

type EvidenceRef struct {
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Scope  string `json:"scope"`
	Status string `json:"status"`
}

type LedgerItem struct {
	ID                    string                 `json:"id"`
	Kind                  string                 `json:"kind"`
	Title                 string                 `json:"title"`
	ClaimScope            string                 `json:"claim_scope"`
	Status                string                 `json:"status"`
	Fulfilled             bool                   `json:"fulfilled"`
	SourceCitations       []Citation             `json:"source_citations"`
	RequiredArtifacts     []string               `json:"required_artifacts"`
	ExpectedCounts        map[string]int         `json:"expected_counts"`
	ArithmeticConstraints []ArithmeticConstraint `json:"arithmetic_constraints"`
	EvidenceRefs          []EvidenceRef          `json:"evidence_refs"`
	Dependencies          []string               `json:"dependencies"`
	Notes                 string                 `json:"notes"`
}

type ArithmeticConstraint struct {
	Name           string `json:"name"`
	LeftKey        string `json:"left_key"`
	Operator       string `json:"operator"`
	RightKey       string `json:"right_key"`
	ResultKey      string `json:"result_key"`
	ExpectedResult int    `json:"expected_result"`
	Status         string `json:"status"`
}

type jsonKind uint8

const (
	kindRoot jsonKind = iota
	kindProvenance
	kindSourceIdentity
	kindAcceptedCommit
	kindExternalSource
	kindCitation
	kindEvidenceRef
	kindLedgerItem
	kindArithmetic
	kindCounts
	kindString
	kindBool
	kindNumber
	kindStatusArray
	kindEntryArray
	kindAcceptedCommitArray
	kindExternalSourceArray
	kindCitationArray
	kindEvidenceRefArray
	kindArithmeticArray
	kindStringArray
	kindDependencyArray
)

type objectRule struct {
	fields       map[string]jsonKind
	dynamicValue jsonKind
	maxFields    int
}

// scanStrictJSON rejects JSON behaviors that encoding/json intentionally
// tolerates: duplicate/case-variant keys, case-insensitive field matching,
// escaped known field spellings, nulls, invalid UTF-8, and unknown fields.
// This is a structural scanner only; it does not attest files, commits, or
// runtime state.
func scanStrictJSON(b []byte) error {
	s := strictScanner{data: b}
	if err := s.value(kindRoot); err != nil {
		return fmt.Errorf("strict JSON scan: %w", err)
	}
	s.space()
	if s.pos != len(s.data) {
		return fmt.Errorf("trailing JSON data at byte %d", s.pos)
	}
	return nil
}

type strictScanner struct {
	data []byte
	pos  int
}

func (s *strictScanner) value(kind jsonKind) error {
	s.space()
	if s.pos >= len(s.data) {
		return errors.New("unexpected end of JSON")
	}
	switch kind {
	case kindRoot, kindProvenance, kindSourceIdentity, kindAcceptedCommit, kindExternalSource, kindCitation, kindEvidenceRef, kindLedgerItem, kindArithmetic, kindCounts:
		rule, ok := objectRuleFor(kind)
		if !ok || s.data[s.pos] != '{' {
			return fmt.Errorf("expected object at byte %d", s.pos)
		}
		return s.object(rule)
	case kindStatusArray:
		return s.array(kindString, true, MaxStatusValues)
	case kindEntryArray:
		return s.array(kindLedgerItem, true, MaxEntries)
	case kindAcceptedCommitArray:
		return s.array(kindAcceptedCommit, true, MaxAcceptedCommits)
	case kindExternalSourceArray:
		return s.array(kindExternalSource, true, MaxExternalSources)
	case kindCitationArray:
		return s.array(kindCitation, true, MaxCitationsPerEntry)
	case kindEvidenceRefArray:
		return s.array(kindEvidenceRef, false, MaxEvidenceRefs)
	case kindArithmeticArray:
		return s.array(kindArithmetic, false, MaxArithmeticConstraints)
	case kindStringArray:
		return s.array(kindString, true, MaxRequiredArtifacts)
	case kindDependencyArray:
		return s.array(kindString, false, MaxDependencies)
	case kindString:
		_, _, err := s.string()
		return err
	case kindBool:
		if s.literal("true") || s.literal("false") {
			return nil
		}
		if s.starts("null") {
			return fmt.Errorf("unexpected null at byte %d", s.pos)
		}
		return fmt.Errorf("expected boolean at byte %d", s.pos)
	case kindNumber:
		return s.number()
	default:
		return fmt.Errorf("unsupported scanner kind %d", kind)
	}
}

func (s *strictScanner) object(rule objectRule) error {
	s.pos++
	seen := make(map[string]bool, len(rule.fields))
	s.space()
	if s.consume('}') {
		if rule.dynamicValue != 0 {
			return nil
		}
		return s.requireFields(rule.fields, seen)
	}
	for {
		if len(seen) >= rule.maxFields {
			return fmt.Errorf("object exceeds %d fields", rule.maxFields)
		}
		s.space()
		raw, key, err := s.string()
		if err != nil {
			return err
		}
		folded := strings.ToLower(key)
		if seen[folded] {
			return fmt.Errorf("duplicate or case-variant key %q", key)
		}
		seen[folded] = true
		fieldKind, known := rule.fields[key]
		if !known {
			if rule.dynamicValue == 0 {
				return fmt.Errorf("unknown field %q", key)
			}
			fieldKind = rule.dynamicValue
		} else if raw != `"`+key+`"` {
			return fmt.Errorf("field %q does not use its exact spelling", key)
		}
		s.space()
		if !s.consume(':') {
			return fmt.Errorf("missing colon after field %q", key)
		}
		if err := s.value(fieldKind); err != nil {
			return fmt.Errorf("field %q: %w", key, err)
		}
		s.space()
		if s.consume('}') {
			break
		}
		if !s.consume(',') {
			return fmt.Errorf("expected comma or object end after field %q", key)
		}
	}
	return s.requireFields(rule.fields, seen)
}

func (s *strictScanner) array(itemKind jsonKind, nonempty bool, maxItems int) error {
	s.pos++
	s.space()
	if s.consume(']') {
		if nonempty {
			return errors.New("unexpected empty array")
		}
		return nil
	}
	count := 0
	for {
		if count >= maxItems {
			return fmt.Errorf("array exceeds %d items", maxItems)
		}
		if err := s.value(itemKind); err != nil {
			return err
		}
		count++
		s.space()
		if s.consume(']') {
			break
		}
		if !s.consume(',') {
			return errors.New("expected comma or array end")
		}
	}
	if nonempty && count == 0 {
		return errors.New("unexpected empty array")
	}
	return nil
}

func (s *strictScanner) requireFields(fields map[string]jsonKind, seen map[string]bool) error {
	for field := range fields {
		if !seen[strings.ToLower(field)] {
			return fmt.Errorf("missing required field %q", field)
		}
	}
	return nil
}

func (s *strictScanner) string() (string, string, error) {
	s.space()
	start := s.pos
	if !s.consume('"') {
		return "", "", fmt.Errorf("expected string at byte %d", s.pos)
	}
	for s.pos < len(s.data) {
		c := s.data[s.pos]
		s.pos++
		switch c {
		case '"':
			raw := string(s.data[start:s.pos])
			if len(raw) > MaxStringBytes {
				return "", "", fmt.Errorf("string exceeds %d bytes", MaxStringBytes)
			}
			var decoded string
			if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
				return "", "", fmt.Errorf("invalid string at byte %d: %w", start, err)
			}
			return raw, decoded, nil
		case '\\':
			if s.pos >= len(s.data) {
				return "", "", errors.New("unterminated string escape")
			}
			if s.data[s.pos] == 'u' {
				if s.pos+4 >= len(s.data) {
					return "", "", errors.New("short unicode escape")
				}
				for _, h := range s.data[s.pos+1 : s.pos+5] {
					if !isHex(h) {
						return "", "", errors.New("invalid unicode escape")
					}
				}
				s.pos += 5
			} else {
				escape := s.data[s.pos]
				if !strings.ContainsRune(`"\\/bfnrt`, rune(escape)) {
					return "", "", errors.New("invalid string escape")
				}
				s.pos++
			}
		case '\n', '\r':
			return "", "", errors.New("unescaped line break in string")
		default:
			if c < 0x20 {
				return "", "", errors.New("control byte in string")
			}
		}
	}
	return "", "", errors.New("unterminated string")
}

func (s *strictScanner) number() error {
	s.space()
	start := s.pos
	for s.pos < len(s.data) {
		c := s.data[s.pos]
		if (c >= '0' && c <= '9') || strings.ContainsRune("-+.eE", rune(c)) {
			s.pos++
			continue
		}
		break
	}
	if start == s.pos || !json.Valid(s.data[start:s.pos]) {
		return fmt.Errorf("invalid number at byte %d", start)
	}
	return nil
}

func (s *strictScanner) literal(value string) bool {
	if !s.starts(value) {
		return false
	}
	s.pos += len(value)
	return true
}

func (s *strictScanner) starts(value string) bool {
	return s.pos+len(value) <= len(s.data) && string(s.data[s.pos:s.pos+len(value)]) == value
}

func (s *strictScanner) consume(want byte) bool {
	if s.pos < len(s.data) && s.data[s.pos] == want {
		s.pos++
		return true
	}
	return false
}

func (s *strictScanner) space() {
	for s.pos < len(s.data) {
		switch s.data[s.pos] {
		case ' ', '\t', '\r', '\n':
			s.pos++
		default:
			return
		}
	}
}

func isHex(value byte) bool {
	return (value >= '0' && value <= '9') || (value >= 'a' && value <= 'f') || (value >= 'A' && value <= 'F')
}

func objectRuleFor(kind jsonKind) (objectRule, bool) {
	all := func(fields ...string) map[string]jsonKind {
		result := make(map[string]jsonKind, len(fields))
		for _, field := range fields {
			result[field] = kindString
		}
		return result
	}
	with := func(fields map[string]jsonKind, name string, value jsonKind) map[string]jsonKind {
		fields[name] = value
		return fields
	}
	switch kind {
	case kindRoot:
		return objectRule{fields: map[string]jsonKind{
			"$schema": kindString, "index_version": kindNumber, "review_date": kindString, "attestation_scope": kindString,
			"provenance": kindProvenance, "status_vocabulary": kindStatusArray, "entries": kindEntryArray,
		}, maxFields: MaxObjectFields}, true
	case kindProvenance:
		return objectRule{fields: map[string]jsonKind{
			"review_scope": kindString, "current_checkout": kindSourceIdentity,
			"accepted_component_commits": kindAcceptedCommitArray, "external_recorded_sources": kindExternalSourceArray,
			"citation_count": kindNumber,
		}, maxFields: MaxObjectFields}, true
	case kindSourceIdentity:
		return objectRule{fields: with(all("commit", "commit_date", "scope"), "commit_date", kindString), maxFields: MaxObjectFields}, true
	case kindAcceptedCommit:
		return objectRule{fields: all("commit", "scope", "status"), maxFields: MaxObjectFields}, true
	case kindExternalSource:
		return objectRule{fields: with(with(with(with(with(all("ref", "base_commit", "base_blob", "working_tree_blob", "sha256", "status"), "host_observation_revalidated", kindBool), "base_commit", kindString), "base_blob", kindString), "working_tree_blob", kindString), "sha256", kindString), maxFields: MaxObjectFields}, true
	case kindCitation:
		return objectRule{fields: all("ref", "source_date", "locator", "lines"), maxFields: MaxObjectFields}, true
	case kindEvidenceRef:
		return objectRule{fields: all("kind", "id", "ref", "scope", "status"), maxFields: MaxObjectFields}, true
	case kindArithmetic:
		return objectRule{fields: with(with(with(with(with(with(all("name", "left_key", "operator", "right_key", "status"), "result_key", kindString), "expected_result", kindNumber), "right_key", kindString), "operator", kindString), "left_key", kindString), "name", kindString), maxFields: MaxObjectFields}, true
	case kindCounts:
		return objectRule{dynamicValue: kindNumber, maxFields: MaxExpectedCountFields}, true
	case kindLedgerItem:
		return objectRule{fields: map[string]jsonKind{
			"id": kindString, "kind": kindString, "title": kindString, "claim_scope": kindString,
			"status": kindString, "fulfilled": kindBool, "source_citations": kindCitationArray,
			"required_artifacts": kindStringArray, "expected_counts": kindCounts,
			"arithmetic_constraints": kindArithmeticArray, "evidence_refs": kindEvidenceRefArray,
			"dependencies": kindDependencyArray, "notes": kindString,
		}, maxFields: MaxObjectFields}, true
	default:
		return objectRule{}, false
	}
}

// ValidateReader validates a bounded JSON stream without accepting a path.
func ValidateReader(r io.Reader) (Document, error) {
	if r == nil {
		return Document{}, errors.New("nil evaluation-index reader")
	}
	b, err := io.ReadAll(io.LimitReader(r, MaxIndexSize+1))
	if err != nil {
		return Document{}, fmt.Errorf("read evaluation index: %w", err)
	}
	if len(b) > MaxIndexSize {
		return Document{}, fmt.Errorf("evaluation index exceeds %d bytes", MaxIndexSize)
	}
	return ValidateJSON(b)
}

// ValidateJSON validates the ledger structure and claim-status invariants.
// It does not resolve citations or inspect the filesystem.
func ValidateJSON(b []byte) (Document, error) {
	if len(b) == 0 {
		return Document{}, errors.New("empty evaluation index")
	}
	if len(b) > MaxIndexSize {
		return Document{}, fmt.Errorf("evaluation index exceeds %d bytes", MaxIndexSize)
	}
	if !utf8.Valid(b) {
		return Document{}, errors.New("evaluation index is not valid UTF-8")
	}
	if err := scanStrictJSON(b); err != nil {
		return Document{}, err
	}
	var d Document
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return Document{}, fmt.Errorf("decode evaluation index: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return Document{}, errors.New("evaluation index contains multiple JSON values")
		}
		return Document{}, fmt.Errorf("trailing evaluation-index data: %w", err)
	}
	if err := validateDocument(d); err != nil {
		return Document{}, err
	}
	return d, nil
}

func validateDocument(d Document) error {
	if d.Schema != Schema {
		return fmt.Errorf("unsupported schema %q", d.Schema)
	}
	if d.IndexVersion != 1 {
		return fmt.Errorf("unsupported index version %d", d.IndexVersion)
	}
	if !validDate(d.ReviewDate) {
		return fmt.Errorf("invalid review_date %q", d.ReviewDate)
	}
	if strings.TrimSpace(d.AttestationScope) == "" {
		return errors.New("missing attestation_scope")
	}
	if strings.TrimSpace(d.Provenance.ReviewScope) == "" {
		return errors.New("missing provenance review_scope")
	}
	if err := validateSourceIdentity("current_checkout", d.Provenance.CurrentCheckout); err != nil {
		return err
	}
	if d.Provenance.CitationCount < 1 {
		return errors.New("citation_count must be positive")
	}
	if len(d.StatusVocabulary) == 0 {
		return errors.New("empty status vocabulary")
	}
	allowed := make(map[string]bool, len(d.StatusVocabulary))
	for _, status := range d.StatusVocabulary {
		if !allowedStatus(status) {
			return fmt.Errorf("unsupported status vocabulary value %q", status)
		}
		if allowed[status] {
			return fmt.Errorf("duplicate status vocabulary value %q", status)
		}
		allowed[status] = true
	}
	if allowed["fulfilled"] || allowed["passed"] || allowed["pass"] || allowed["complete"] {
		return errors.New("status vocabulary permits an inflated completion status")
	}

	accepted := make(map[string]bool, len(d.Provenance.AcceptedComponentCommits))
	for _, commit := range d.Provenance.AcceptedComponentCommits {
		if !hex40.MatchString(commit.Commit) {
			return fmt.Errorf("accepted component commit is not a full Git identity: %q", commit.Commit)
		}
		if commit.Status != "accepted-component" || strings.TrimSpace(commit.Scope) == "" {
			return fmt.Errorf("accepted component commit %q lacks accepted-component scope/status", commit.Commit)
		}
		if accepted[commit.Commit] {
			return fmt.Errorf("duplicate accepted component commit %q", commit.Commit)
		}
		accepted[commit.Commit] = true
	}
	for _, source := range d.Provenance.ExternalRecordedSources {
		if err := validateLogicalRef(source.Ref); err != nil {
			return fmt.Errorf("external source: %w", err)
		}
		if !hex40.MatchString(source.BaseCommit) || !hex40.MatchString(source.BaseBlob) || !hex40.MatchString(source.WorkingTreeBlob) {
			return fmt.Errorf("external source %q has incomplete Git identities", source.Ref)
		}
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(strings.ToLower(source.SHA256)) {
			return fmt.Errorf("external source %q has invalid SHA-256", source.Ref)
		}
		if !strings.HasPrefix(source.Status, "external-recorded-source-only") || source.HostObservationRevalidated {
			return fmt.Errorf("external source %q is not marked un-revalidated", source.Ref)
		}
	}
	externalRefs := make(map[string]bool, len(d.Provenance.ExternalRecordedSources))
	for _, source := range d.Provenance.ExternalRecordedSources {
		if externalRefs[source.Ref] {
			return fmt.Errorf("duplicate external recorded source %q", source.Ref)
		}
		externalRefs[source.Ref] = true
	}

	ids := make(map[string]bool, len(d.Entries))
	citationCount := 0
	for _, entry := range d.Entries {
		if ids[entry.ID] {
			return fmt.Errorf("duplicate ledger entry %q", entry.ID)
		}
		ids[entry.ID] = true
		if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.Title) == "" || strings.TrimSpace(entry.ClaimScope) == "" {
			return errors.New("ledger entry is missing id, title, or claim_scope")
		}
		if entry.Kind != "freeze" && entry.Kind != "study" && entry.Kind != "unlock" {
			return fmt.Errorf("entry %q has unsupported kind %q", entry.ID, entry.Kind)
		}
		if !allowed[entry.Status] {
			return fmt.Errorf("entry %q uses status %q outside the vocabulary", entry.ID, entry.Status)
		}
		if entry.Fulfilled {
			return fmt.Errorf("entry %q cannot claim fulfilled", entry.ID)
		}
		if isInflatedStatus(entry.Status) {
			return fmt.Errorf("entry %q uses inflated completion status %q", entry.ID, entry.Status)
		}
		if len(entry.SourceCitations) == 0 {
			return fmt.Errorf("entry %q has no pinpoint source citation", entry.ID)
		}
		for _, citation := range entry.SourceCitations {
			if err := validateCitation(citation); err != nil {
				return fmt.Errorf("entry %q citation: %w", entry.ID, err)
			}
		}
		citationCount += len(entry.SourceCitations)
		for _, artifact := range entry.RequiredArtifacts {
			if !validRelativeArtifact(artifact) {
				return fmt.Errorf("entry %q has unsafe required artifact %q", entry.ID, artifact)
			}
		}
		for name, count := range entry.ExpectedCounts {
			if strings.TrimSpace(name) == "" || count < 0 {
				return fmt.Errorf("entry %q has invalid expected count %q=%d", entry.ID, name, count)
			}
		}
		if err := validateArithmetic(entry); err != nil {
			return fmt.Errorf("entry %q arithmetic: %w", entry.ID, err)
		}
		for _, ref := range entry.EvidenceRefs {
			if err := validateEvidenceRef(ref, accepted, externalRefs); err != nil {
				return fmt.Errorf("entry %q evidence: %w", entry.ID, err)
			}
		}
	}
	if citationCount != d.Provenance.CitationCount {
		return fmt.Errorf("citation_count=%d does not match %d citations", d.Provenance.CitationCount, citationCount)
	}
	for _, entry := range d.Entries {
		for _, dependency := range entry.Dependencies {
			if !ids[dependency] {
				return fmt.Errorf("entry %q depends on unknown entry %q", entry.ID, dependency)
			}
		}
	}
	return nil
}

func validateSourceIdentity(name string, source SourceIdentity) error {
	if !hex40.MatchString(source.Commit) {
		return fmt.Errorf("%s has invalid full Git identity %q", name, source.Commit)
	}
	if !validDate(source.CommitDate) || strings.TrimSpace(source.Scope) == "" {
		return fmt.Errorf("%s lacks dated scope", name)
	}
	return nil
}

func validateCitation(c Citation) error {
	if err := validateLogicalRef(c.Ref); err != nil {
		return err
	}
	if !validDate(c.SourceDate) || strings.TrimSpace(c.Locator) == "" || !validLineRange(c.Lines) {
		return fmt.Errorf("citation %q lacks a dated locator/line range", c.Ref)
	}
	return nil
}

func validateEvidenceRef(ref EvidenceRef, accepted map[string]bool, externalRefs map[string]bool) error {
	if strings.TrimSpace(ref.Scope) == "" || strings.TrimSpace(ref.Status) == "" {
		return errors.New("evidence reference lacks scope/status")
	}
	switch ref.Kind {
	case "accepted-commit":
		if !accepted[ref.ID] {
			return fmt.Errorf("commit %q is not in accepted component identities", ref.ID)
		}
		if ref.Status != "component-only" {
			return fmt.Errorf("accepted commit %q has unsupported claim status %q", ref.ID, ref.Status)
		}
		if ref.Ref != "" {
			if err := validateLogicalRef(ref.Ref); err != nil {
				return err
			}
		}
	case "recorded-source":
		if err := validateLogicalRef(ref.Ref); err != nil {
			return err
		}
		if !externalRefs[ref.Ref] {
			return fmt.Errorf("recorded source %q is not registered in external_recorded_sources", ref.Ref)
		}
		if ref.Status != "external-recorded-source-only" {
			return fmt.Errorf("recorded source %q is not external-only", ref.Ref)
		}
	default:
		return fmt.Errorf("unsupported evidence kind %q", ref.Kind)
	}
	return nil
}

func validateLogicalRef(ref string) error {
	if !(strings.HasPrefix(ref, "repo:") || strings.HasPrefix(ref, "thesis:")) {
		return fmt.Errorf("unsafe/nonlogical source reference %q", ref)
	}
	path := ref[strings.IndexByte(ref, ':')+1:]
	if !validRelativeLogicalPath(path) {
		return fmt.Errorf("unsafe source reference %q", ref)
	}
	return nil
}

func validDate(value string) bool {
	if t, err := time.Parse("2006-01-02", value); err == nil && t.Format("2006-01-02") == value {
		return true
	}
	return false
}

func allowedStatus(value string) bool {
	return value == "blocked" || value == "partial-component" || value == "not-evidenced" || value == "component-accepted"
}

func isInflatedStatus(value string) bool {
	return value == "fulfilled" || value == "passed" || value == "pass" || value == "complete" || value == "accepted"
}

func validRelativeLogicalPath(path string) bool {
	if path == "" || strings.ContainsAny(path, "\\\x00") || strings.Contains(path, ":") || strings.Contains(path, "://") || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validRelativeArtifact(value string) bool {
	if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\\\x00") || strings.Contains(value, ":") || strings.Contains(value, "://") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == ".." || part == "." {
			return false
		}
	}
	return true
}

func validLineRange(value string) bool {
	if !lineRange.MatchString(value) {
		return false
	}
	previousStart := 0
	for _, segment := range strings.Split(value, ";") {
		parts := strings.SplitN(segment, "-", 2)
		start, _ := strconv.Atoi(parts[0])
		end := start
		if len(parts) == 2 {
			end, _ = strconv.Atoi(parts[1])
		}
		if start < 1 || end < start || (previousStart != 0 && start < previousStart) {
			return false
		}
		previousStart = start
	}
	return true
}

func validateArithmetic(entry LedgerItem) error {
	for _, constraint := range entry.ArithmeticConstraints {
		if strings.TrimSpace(constraint.Name) == "" || strings.TrimSpace(constraint.LeftKey) == "" || strings.TrimSpace(constraint.RightKey) == "" || strings.TrimSpace(constraint.ResultKey) == "" {
			return errors.New("arithmetic constraint has an empty identity field")
		}
		left, leftOK := entry.ExpectedCounts[constraint.LeftKey]
		right, rightOK := entry.ExpectedCounts[constraint.RightKey]
		result, resultOK := entry.ExpectedCounts[constraint.ResultKey]
		if !leftOK || !rightOK || !resultOK {
			return fmt.Errorf("constraint %q references missing expected count", constraint.Name)
		}
		if left < 0 || right < 0 || result < 0 || constraint.ExpectedResult < 0 {
			return fmt.Errorf("constraint %q requires nonnegative integer values", constraint.Name)
		}
		if constraint.Status != "declared" && constraint.Status != "source-ambiguity" {
			return fmt.Errorf("constraint %q has unsupported status %q", constraint.Name, constraint.Status)
		}
		calculated := 0
		switch constraint.Operator {
		case "+":
			var ok bool
			calculated, ok = checkedNonnegativeAdd(left, right)
			if !ok {
				return fmt.Errorf("constraint %q addition overflows int", constraint.Name)
			}
		case "*":
			var ok bool
			calculated, ok = checkedNonnegativeMultiply(left, right)
			if !ok {
				return fmt.Errorf("constraint %q multiplication overflows int", constraint.Name)
			}
		default:
			return fmt.Errorf("constraint %q has unsupported operator %q", constraint.Name, constraint.Operator)
		}
		if calculated != result || calculated != constraint.ExpectedResult {
			return fmt.Errorf("constraint %q does not match: got %d, expected %d/result %d", constraint.Name, calculated, constraint.ExpectedResult, result)
		}
	}
	return nil
}

func checkedNonnegativeAdd(left, right int) (int, bool) {
	if left < 0 || right < 0 {
		return 0, false
	}
	maxInt := int(^uint(0) >> 1)
	if left > maxInt-right {
		return 0, false
	}
	return left + right, true
}

func checkedNonnegativeMultiply(left, right int) (int, bool) {
	if left < 0 || right < 0 {
		return 0, false
	}
	if left == 0 || right == 0 {
		return 0, true
	}
	maxInt := int(^uint(0) >> 1)
	if left > maxInt/right {
		return 0, false
	}
	return left * right, true
}
