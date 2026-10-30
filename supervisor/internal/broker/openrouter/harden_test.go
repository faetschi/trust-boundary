package openrouter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const researchDatedSlug = "deepseek/deepseek-v4.1-flash-20260910"

func TestRequestHeadersAreRouteSpecific(t *testing.T) {
	engineering := EngineeringRoute().RequestHeaders("session-1")
	if !reflect.DeepEqual(map[string][]string(engineering), map[string][]string{
		"Accept": {"text/event-stream"}, "Content-Type": {"application/json"}, "User-Agent": {UserAgent}, SessionHeader: {"session-1"}}) {
		t.Fatalf("engineering headers changed: %v", engineering)
	}
	research := researchTestRoute(t).RequestHeaders("session-1")
	if !reflect.DeepEqual(map[string][]string(research), map[string][]string{"Content-Type": {"application/json"}}) {
		t.Fatalf("research headers = %v; want Content-Type only", research)
	}
}

func TestResearchTransportRefusesVendorHeadersAndEngineeringTransportRequiresThem(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	key := func() (string, error) { return "test-key-123", nil }
	research, err := NewHTTPDoerForEndpoint(key, time.Second, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	build := func(headers http.Header) *http.Request {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		request.Header = headers
		return request
	}
	if _, err := research.Do(build(researchTestRoute(t).RequestHeaders("session-1"))); err != nil {
		t.Fatalf("research headers refused: %v", err)
	}
	if seen.Get(SessionHeader) != "" || seen.Get("Authorization") == "" {
		t.Fatalf("research transport sent %v", seen)
	}
	if _, err := research.Do(build(EngineeringRoute().RequestHeaders("session-1"))); err == nil {
		t.Fatal("research transport accepted the engineering header set")
	}
	engineering, err := newHTTPDoer(key, time.Second, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engineering.Do(build(researchTestRoute(t).RequestHeaders("session-1"))); err == nil {
		t.Fatal("engineering transport accepted the research header set")
	}
}

func TestResponseModelListDefaultsToTheRequestedSlugAndIsExact(t *testing.T) {
	route := researchTestRoute(t)
	if got := route.ResponseModels(); len(got) != 1 || got[0] != researchTestModel {
		t.Fatalf("default list = %v", got)
	}
	if !route.AcceptsResponseModel(researchTestModel) || route.AcceptsResponseModel(researchDatedSlug) || route.AcceptsResponseModel("") {
		t.Fatal("default list is not just the requested slug")
	}
	if !EngineeringRoute().AcceptsResponseModel(ApprovedModel) || (Route{}).AcceptsResponseModel(researchTestModel) {
		t.Fatal("engineering route response model drifted")
	}
	pinned, err := NewResearchRoute(ResearchSpec{Model: researchTestModel, Endpoint: researchTestEndpoint,
		ProfileDigest: researchTestDigest, Members: researchTestMembers, Timeout: time.Second,
		ResponseModels: []string{researchTestModel, researchDatedSlug}})
	if err != nil {
		t.Fatal(err)
	}
	if !pinned.AcceptsResponseModel(researchDatedSlug) || pinned.AcceptsResponseModel(researchDatedSlug+"x") || pinned == route || pinned.Validate() != nil {
		t.Fatal("pinned list is not exact, comparable and valid")
	}
	for name, list := range map[string][]string{
		"duplicate": {researchTestModel, researchTestModel}, "empty entry": {""}, "padded": {" a/b"},
		"newline": {"a/b\nc/d"}, "too many": {"a/1", "a/2", "a/3", "a/4", "a/5", "a/6", "a/7", "a/8", "a/9"},
	} {
		if _, err := NewResearchRoute(ResearchSpec{Model: researchTestModel, Endpoint: researchTestEndpoint,
			ProfileDigest: researchTestDigest, Members: researchTestMembers, Timeout: time.Second, ResponseModels: list}); err == nil {
			t.Errorf("route accepted a %s list", name)
		}
	}
}
