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

// HintTransport pins a chooser route and, for local inference, its model bytes and decoding profile.
type HintTransport struct {
	Decoding    string `json:"decoding,omitempty"`
	Kind        string `json:"kind"`
	Endpoint    string `json:"endpoint"`
	ModelDigest string `json:"model_digest,omitempty"`
}

// CDHManifest pins the secondary treatment. C/static remains H1; an unresolved second model is invalid.
type CDHManifest struct {
	SchemaVersion string                       `json:"schema_version"`
	Arm           string                       `json:"arm"`
	Transports    []HintTransport              `json:"transports"`
	Models        []string                     `json:"models"`
	CandidateSet  string                       `json:"candidate_set"`
	Candidates    map[string]map[string]string `json:"candidates"`
	InputFields   []string                     `json:"input_fields"`
	MinConfidence float64                      `json:"min_confidence"`
	TimeoutMS     int64                        `json:"timeout_ms"`
}

func NewCDHManifest(openSourceModel string, confidence float64, timeout time.Duration) (CDHManifest, error) {
	m := CDHManifest{SchemaVersion: "tbound-cdh-manifest/v1", Arm: "C-DH",
		Models: []string{"typesafe/jev-1.14", openSourceModel}, CandidateSet: hintCandidateSet,
		Transports: []HintTransport{{Kind: "decisions", Endpoint: "https://openrouter.ai/api/alpha"}, {Kind: "decisions", Endpoint: "https://openrouter.ai/api/alpha"}},
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
	if m.SchemaVersion != "tbound-cdh-manifest/v1" || m.Arm != "C-DH" || len(m.Models) != 2 || len(m.Transports) != 2 ||
		m.Models[0] != "typesafe/jev-1.14" || !validText(m.Models[1], MaxIdentityBytes) || m.Models[0] == m.Models[1] ||
		m.CandidateSet != hintCandidateSet || !reflect.DeepEqual(m.Candidates, frozenHintCandidates()) ||
		!reflect.DeepEqual(m.InputFields, []string{"reason_code", "tool", "arguments", "task"}) ||
		math.IsNaN(m.MinConfidence) || m.MinConfidence <= 0 || m.MinConfidence > 1 ||
		m.TimeoutMS <= 0 || m.TimeoutMS > maxHintTimeout.Milliseconds() {
		return "", errors.New("invalid or unresolved C-DH treatment manifest")
	}
	for i, transport := range m.Transports {
		if i == 0 && transport.Kind != "decisions" {
			return "", errors.New("Jev requires the decisions transport")
		}
		switch transport.Kind {
		case "decisions":
			if !validDecisionsEndpoint(transport.Endpoint) || (transport.ModelDigest != "" || transport.Decoding != "") {
				return "", errors.New("invalid decisions transport manifest")
			}
		case "local":
			if !validLocalEndpoint(transport.Endpoint) || (!validModelFileDigest(transport.ModelDigest) || transport.Decoding != "candidate-id-greedy/v1") {
				return "", errors.New("invalid local transport manifest")
			}
		default:
			return "", errors.New("unknown chooser transport")
		}
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
func NewCDHProvider(m CDHManifest, cfg ChooserConfig) (ChoiceHintProvider, error) {
	digest, err := m.Digest()
	if err != nil {
		return ChoiceHintProvider{}, err
	}
	if cfg.Model != m.Models[0] && cfg.Model != m.Models[1] {
		return ChoiceHintProvider{}, errors.New("chooser model is not registered in C-DH")
	}
	index := 0
	if cfg.Model == m.Models[1] {
		index = 1
	}
	cfg.Kind, cfg.BaseURL, cfg.ModelDigest = m.Transports[index].Kind, m.Transports[index].Endpoint, m.Transports[index].ModelDigest
	cfg.MinConfidence, cfg.Timeout = m.MinConfidence, time.Duration(m.TimeoutMS)*time.Millisecond
	p := NewChoiceHintProvider(cfg)
	p.ManifestDigest = digest
	return p, nil
}

// NewCDHLocalManifest pins a local open-weight chooser without guessing a model or file identity.
func NewCDHLocalManifest(model, endpoint, fileDigest string, confidence float64, timeout time.Duration) (CDHManifest, error) {
	m, _ := NewCDHManifest(model, confidence, timeout)
	m.Transports[1] = HintTransport{Kind: "local", Endpoint: endpoint, ModelDigest: fileDigest, Decoding: "candidate-id-greedy/v1"}
	_, err := m.Digest()
	return m, err
}
