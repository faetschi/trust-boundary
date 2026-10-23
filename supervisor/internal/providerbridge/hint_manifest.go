package providerbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"time"
)

// CDHManifest is the complete preregistration treatment, independent of credentials and endpoint routing.
// C remains static H1. An unresolved second model is deliberately not a valid C-DH manifest.
type CDHManifest struct {
	SchemaVersion string                       `json:"schema_version"`
	Arm           string                       `json:"arm"`
	Models        [2]string                    `json:"models"`
	CandidateSet  string                       `json:"candidate_set"`
	Candidates    map[string]map[string]string `json:"candidates"`
	InputFields   []string                     `json:"input_fields"`
	MinConfidence float64                      `json:"min_confidence"`
	TimeoutMS     int64                        `json:"timeout_ms"`
}

func NewCDHManifest(openSourceModel string, confidence float64, timeout time.Duration) (CDHManifest, error) {
	m := CDHManifest{SchemaVersion: "tbound-cdh-manifest/v1", Arm: "C-DH",
		Models: [2]string{"typesafe/jev-1.14", openSourceModel}, CandidateSet: hintCandidateSet,
		Candidates: frozenHintCandidates(), InputFields: []string{"reason_code", "tool", "arguments", "task"},
		MinConfidence: confidence, TimeoutMS: timeout.Milliseconds()}
	_, err := m.Digest()
	return m, err
}

func frozenHintCandidates() map[string]map[string]string {
	out := make(map[string]map[string]string, len(permittedAlternativeHints))
	for reason := range permittedAlternativeHints {
		out[reason] = HintCandidates(reason)
	}
	return out
}

// Digest validates all frozen fields before hashing deterministic JSON. No digest field hashes itself.
func (m CDHManifest) Digest() (string, error) {
	if m.SchemaVersion != "tbound-cdh-manifest/v1" || m.Arm != "C-DH" ||
		m.Models[0] != "typesafe/jev-1.14" || !validText(m.Models[1], MaxIdentityBytes) || m.Models[0] == m.Models[1] ||
		m.CandidateSet != hintCandidateSet || !reflect.DeepEqual(m.Candidates, frozenHintCandidates()) ||
		!reflect.DeepEqual(m.InputFields, []string{"reason_code", "tool", "arguments", "task"}) ||
		math.IsNaN(m.MinConfidence) || m.MinConfidence <= 0 || m.MinConfidence > 1 ||
		m.TimeoutMS <= 0 || m.TimeoutMS > maxHintTimeout.Milliseconds() {
		return "", errors.New("invalid or unresolved C-DH treatment manifest")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("tbound/cdh-manifest/v1\x00"), raw...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// NewCDHProvider binds the selected model and all experimental controls to the manifest.
// Endpoint and transport are supplied only after the owner approves and funds the route.
func NewCDHProvider(m CDHManifest, cfg DecisionsConfig) (ChoiceHintProvider, error) {
	digest, err := m.Digest()
	if err != nil {
		return ChoiceHintProvider{}, err
	}
	if cfg.Model != m.Models[0] && cfg.Model != m.Models[1] {
		return ChoiceHintProvider{}, errors.New("chooser model is not registered in C-DH")
	}
	cfg.MinConfidence, cfg.Timeout = m.MinConfidence, time.Duration(m.TimeoutMS)*time.Millisecond
	p := NewChoiceHintProvider(cfg)
	p.ManifestDigest = digest
	return p, nil
}
