package productprovider

import (
	"encoding/json"
	"path"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/openrouter"
)

func TestProfileTable(t *testing.T) {
	profiles := Advertised()
	if len(profiles) != 1 {
		t.Fatalf("advertised %d profiles", len(profiles))
	}
	p := profiles[0]
	if p.Schema != Schema || p.ID != OpenCodeGo || p.Endpoint != openrouter.Endpoint || p.Protocol != "chat-completions-sse" || p.Model != "deepseek-v4.1-flash" || !reflect.DeepEqual(p.ResponseModels, []string{p.Model}) || p.Pricing != NoPriceTable || p.TimeoutSeconds != 90 || p.MinCompletionTokens != 1 || p.MaxCompletionTokens != 2147483647 {
		t.Fatalf("unexpected profile: %+v", p)
	}
	for _, unknown := range []string{"", "openrouter", "../key", "https://elsewhere.invalid"} {
		if _, ok := Lookup(unknown); ok {
			t.Fatalf("accepted %q", unknown)
		}
		if _, ok := CredentialPath(unknown); ok {
			t.Fatal("unsafe credential path")
		}
	}
	p.ResponseModels[0] = "changed"
	profiles[0].Endpoint = "http://elsewhere.invalid"
	again, _ := Lookup(OpenCodeGo)
	if again.ResponseModels[0] != openrouter.ApprovedModel || again.Endpoint != openrouter.Endpoint {
		t.Fatal("profile mutation changed table")
	}
	if _, err := json.Marshal(Advertised()); err != nil {
		t.Fatal(err)
	}
}

func TestProductCredentialPathSafety(t *testing.T) {
	p, ok := CredentialPath(OpenCodeGo)
	if !ok || p != "/etc/tbound-product/credentials/opencode-go-engineering.key" {
		t.Fatalf("path %q", p)
	}
	// Compare whole path components: tbound-product is not the thesis tbound.
	for _, forbidden := range []string{"/etc/tbound", "/var/lib/tbound", "/run/tbound", "/run/tbound-pi"} {
		if path.Clean(p) == forbidden || strings.HasPrefix(path.Clean(p), forbidden+"/") {
			t.Fatalf("product credential inside thesis path %s", forbidden)
		}
		if CredentialDirectory == forbidden || strings.HasPrefix(CredentialDirectory, forbidden+"/") {
			t.Fatal("unsafe credential directory")
		}
	}
}
