package openrouter

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProductCheckTransportDoesNotFollowRedirect(t *testing.T) {
	const key = "P7-transport-sentinel-key-never-disclose"
	c, err := CredentialsFromKey([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	doer, err := c.NewCheckHTTPDoer(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("missing authorization")
		}
		w.Header().Set("Location", "/never-follow/"+key)
		w.WriteHeader(307)
		fmt.Fprint(w, key)
	}))
	defer server.Close()
	// In-package test binding only; no public product endpoint override.
	doer.endpoint = server.URL
	r, _ := http.NewRequest("POST", server.URL, strings.NewReader("{}"))
	r.Header = EngineeringRoute().RequestHeaders("test")
	response, err := doer.Do(r)
	if err != nil || response.StatusCode != 307 {
		t.Fatalf("redirect: %v", err)
	}
	defer response.Body.Close()
	if calls != 1 {
		t.Fatalf("redirect followed: %d calls", calls)
	}
	if r.Header.Get("Authorization") != "" {
		t.Fatal("credential attached to caller request")
	}
}
