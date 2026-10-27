package openrouter

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"tbound/supervisor/internal/broker/protocol"
)

// researchEndpoint is the one OpenRouter chat-completions URL a research route
// may name. It is repeated here (rather than imported from researchprofile) so
// that the wire package keeps no dependency on the profile package; the
// researchroute package's tests pin the two constants to each other.
const researchEndpoint = "https://openrouter.ai/api/v1/chat/completions"

// maxResearchMembersBytes bounds the frozen request members a route may carry.
const maxResearchMembersBytes = 4 << 10

// reservedResearchMembers are the request members the builder owns. A research
// profile cannot override them.
var reservedResearchMembers = []string{"model", "messages", "tools", "stream", "parallel_tool_calls",
	"tool_choice", "thinking", "max_completion_tokens", "max_tokens"}

// Route selects the wire profile of one provider conversation: its model, its
// chat-completions URL and, for the research route, the members that the frozen
// research profile adds to the request. The zero Route is the fixed engineering
// (OpenCode Go) route, so every existing caller keeps its behaviour. A Route is
// comparable; it is part of the immutable profile that recovery pins compare.
type Route struct {
	Model    string
	Endpoint string
	// ProfileDigest is the lowercase hex SHA-256 of the frozen research profile
	// that produced Members. It is empty on the engineering route.
	ProfileDigest string
	// Timeout is the HTTP timeout of the research profile.
	Timeout time.Duration
	// CommandOutput marks a route whose executor releases bounded Bash stdout and stderr to the provider
	// (a product turn). Its Bash tool description says so. The zero value is an executor that returns the
	// exit code alone (serve --pi), so its description promises no output.
	CommandOutput bool
	members       string
	// responseModels is the newline-joined exact list of response-model strings the
	// route accepts (a string, so that Route stays comparable). The zero route
	// accepts only Model.
	responseModels string
}

// ResearchSpec is the input of NewResearchRoute.
type ResearchSpec struct {
	Model         string
	Endpoint      string
	ProfileDigest string
	// Members is the canonical JSON object of request members fixed by the
	// profile (provider routing, temperature, seed, reasoning).
	Members string
	Timeout time.Duration
	// ResponseModels lists the exact response-model strings the route accepts. Empty
	// means just Model.
	ResponseModels []string
}

// EngineeringRoute is the fixed OpenCode Go route.
func EngineeringRoute() Route { return Route{Model: ApprovedModel, Endpoint: Endpoint} }

// Resolved maps the zero Route to the engineering route.
func (r Route) Resolved() Route {
	if r == (Route{}) {
		return EngineeringRoute()
	}
	return r
}

// WithCommandOutput returns the resolved route with its CommandOutput mark set to on.
func (r Route) WithCommandOutput(on bool) Route {
	r = r.Resolved()
	r.CommandOutput = on
	return r
}

// IsResearch reports whether the route carries frozen research members.
func (r Route) IsResearch() bool { return r.members != "" }

// Members returns the frozen request members of a research route.
func (r Route) Members() string { return r.members }

// NewResearchRoute validates and builds a research route. The endpoint must be
// the OpenRouter chat-completions URL; plain HTTP is accepted only on a loopback
// address, which exists so that offline fixtures can run a fake server. The
// model must not be the engineering model, and the members must be a compact
// JSON object that does not override any member the builder owns.
func NewResearchRoute(spec ResearchSpec) (Route, error) {
	if spec.Model == "" || spec.Model == ApprovedModel || !validText(spec.Model, maxText) {
		return Route{}, errors.New("research route model is missing or is the engineering model")
	}
	if spec.Endpoint != researchEndpoint && !loopbackHTTPEndpoint(spec.Endpoint) {
		return Route{}, errors.New("research route endpoint must be the OpenRouter chat-completions URL")
	}
	if !lowerHex64(spec.ProfileDigest) {
		return Route{}, errors.New("research route needs the 64-hex profile digest")
	}
	if spec.Timeout <= 0 || spec.Timeout > MaxHTTPTimeout {
		return Route{}, errors.New("research route timeout is outside the permitted bound")
	}
	if err := validateResearchMembers(spec.Members); err != nil {
		return Route{}, err
	}
	models := spec.ResponseModels
	if len(models) == 0 {
		models = []string{spec.Model}
	}
	if err := validateResponseModels(models); err != nil {
		return Route{}, err
	}
	return Route{Model: spec.Model, Endpoint: spec.Endpoint, ProfileDigest: spec.ProfileDigest,
		Timeout: spec.Timeout, members: spec.Members, responseModels: strings.Join(models, "\n")}, nil
}

// Validate checks that a route is exactly the engineering route or a well-formed
// research route. A route assembled by hand, such as a different engineering
// model or a research route without members, is refused.
func (r Route) Validate() error {
	r = r.Resolved()
	if !r.IsResearch() {
		if r.WithCommandOutput(false) != EngineeringRoute() {
			return errors.New("provider route is outside the fixed OpenCode Go profile")
		}
		return nil
	}
	checked, err := NewResearchRoute(ResearchSpec{Model: r.Model, Endpoint: r.Endpoint,
		ProfileDigest: r.ProfileDigest, Members: r.members, Timeout: r.Timeout, ResponseModels: r.ResponseModels()})
	if err != nil || checked != r {
		return errors.New("provider route is not a valid research route")
	}
	return nil
}

// maxResponseModels bounds the response-model list of a research route.
const maxResponseModels = 8

// ResponseModels returns the exact response-model strings the route accepts.
func (r Route) ResponseModels() []string {
	r = r.Resolved()
	if r.responseModels == "" {
		return []string{r.Model}
	}
	return strings.Split(r.responseModels, "\n")
}

// AcceptsResponseModel reports whether a provider response may name model: an
// exact match against the route's list, which is just the requested model unless
// the research profile pins more. Nothing else is accepted.
func (r Route) AcceptsResponseModel(model string) bool {
	if model == "" {
		return false
	}
	return slices.Contains(r.ResponseModels(), model)
}

// RequestHeaders is the complete header set the host puts on a provider request
// (the transport adds Authorization). The engineering route keeps its fixed
// OpenCode Go set. The research route sends only Content-Type: no OpenCode
// session header, Accept or host User-Agent reaches OpenRouter, and the profile
// declares no attribution headers.
func (r Route) RequestHeaders(sessionID string) http.Header {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	if r.Resolved().IsResearch() {
		return header
	}
	header.Set("Accept", "text/event-stream")
	header.Set("User-Agent", UserAgent)
	header.Set(SessionHeader, sessionID)
	return header
}

func validateResponseModels(models []string) error {
	if len(models) > maxResponseModels {
		return errors.New("research route response-model list is too long")
	}
	for i, model := range models {
		if !validText(model, maxText) {
			return errors.New("research route response model is not valid text")
		}
		if slices.Contains(models[:i], model) {
			return errors.New("research route response-model list repeats an entry")
		}
	}
	return nil
}

func validateResearchMembers(members string) error {
	if len(members) < 2 || len(members) > maxResearchMembersBytes || members[0] != '{' || members[len(members)-1] != '}' {
		return errors.New("research route members must be one compact JSON object")
	}
	if err := protocol.ValidateStrictJSON([]byte(members)); err != nil {
		return errors.New("research route members are not strict JSON")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(members)); err != nil || compact.String() != members {
		return errors.New("research route members must be compact JSON")
	}
	top, err := object([]byte(members))
	if err != nil || len(top) == 0 {
		return errors.New("research route members must be a non-empty JSON object")
	}
	for _, reserved := range reservedResearchMembers {
		if _, ok := top[reserved]; ok {
			return errors.New("research route members override the request member " + reserved)
		}
	}
	return nil
}

func lowerHex64(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// loopbackHTTPEndpoint reports whether endpoint is an http URL on a loopback IP
// address with no credentials.
func loopbackHTTPEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed == nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Host == "" {
		return false
	}
	ip := net.ParseIP(parsed.Hostname())
	return ip != nil && ip.IsLoopback()
}
