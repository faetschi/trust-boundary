package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPDoerAddsOneBearerHeaderAndDoesNotMutateRequest(t *testing.T) {
	const key = "test-provider-key-123"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		values := request.Header.Values("Authorization")
		if len(values) != 1 || values[0] != "Bearer "+key {
			t.Error("Authorization was not exactly one expected Bearer value")
		}
		_, _ = io.WriteString(writer, "provider echoed "+key)
	}))
	defer server.Close()

	doer, err := newHTTPDoer(func() (string, error) { return key, nil }, time.Second, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header["Authorization"] = []string{"old value", "duplicate value"}
	request.Header["authorization"] = []string{"lowercase duplicate"}
	response, err := doer.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close response: %v / %v", readErr, closeErr)
	}
	if bytes.Contains(body, []byte(key)) || !bytes.Contains(body, []byte("[REDACTED]")) {
		t.Fatal("response credential was not redacted")
	}
	if got := request.Header.Values("Authorization"); len(got) != 2 || got[0] != "old value" || got[1] != "duplicate value" ||
		len(request.Header["authorization"]) != 1 || request.Header["authorization"][0] != "lowercase duplicate" {
		t.Fatalf("transport mutated the registered request headers: %q", got)
	}
}

func TestHTTPDoerRefusesRedirects(t *testing.T) {
	var targetRequests atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/target" {
			targetRequests.Add(1)
			writer.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(writer, request, server.URL+"/target", http.StatusFound)
	}))
	defer server.Close()
	doer, err := newHTTPDoer(func() (string, error) { return "test-key-123", nil }, time.Second, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := doer.Do(testRequest(t, server.URL))
	if err == nil || response != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		t.Fatal("redirect was followed or not rejected")
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target received %d requests", targetRequests.Load())
	}
	if strings.Contains(err.Error(), "test-key-123") {
		t.Fatal("transport error exposed the credential")
	}
}

func TestHTTPDoerAppliesTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(writer, "late")
	}))
	defer server.Close()
	doer, err := newHTTPDoer(func() (string, error) { return "test-key-123", nil }, 35*time.Millisecond, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response, err := doer.Do(testRequest(t, server.URL))
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("slow response did not time out")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("configured timeout was not bounded: elapsed %s", elapsed)
	}
	if strings.Contains(err.Error(), "test-key-123") {
		t.Fatal("timeout error exposed the credential")
	}
}

func TestHTTPDoerRedactsCredentialAcrossResponseReadBoundaries(t *testing.T) {
	const key = "boundary-sensitive-key-987"
	content := strings.Repeat("x", (32<<10)-3) + key + "-tail"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, content)
	}))
	defer server.Close()
	doer, err := newHTTPDoer(func() (string, error) { return key, nil }, time.Second, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := doer.Do(testRequest(t, server.URL))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if bytes.Contains(body, []byte(key)) || !bytes.Contains(body, []byte("[REDACTED]")) {
		t.Fatal("credential crossing a response read boundary was not redacted")
	}
}

func TestLoadCredentialsEnvironmentPrecedesFileAndRedactsRepresentations(t *testing.T) {
	const secret = "env-openrouter-secret-987"
	credentials, err := LoadCredentialsFrom(mapLookup(map[string]string{
		ModelEnv:      "vendor/model:private",
		APIKeyEnv:     secret,
		APIKeyFileEnv: filepath.Join(t.TempDir(), "must-not-be-opened"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if credentials.ModelID() != "vendor/model:private" {
		t.Fatalf("model = %q", credentials.ModelID())
	}
	for name, rendered := range map[string]string{
		"string":   credentials.String(),
		"gostring": fmt.Sprintf("%#v", credentials),
		"json":     string(mustJSON(t, credentials)),
	} {
		if strings.Contains(rendered, secret) || !strings.Contains(rendered, "[REDACTED]") {
			t.Errorf("%s representation was not redacted", name)
		}
	}
	if got := credentials.Redact("response=" + secret); strings.Contains(got, secret) || !strings.Contains(got, "[REDACTED]") {
		t.Fatal("Redact did not replace the credential")
	}
}

func TestLoadCredentialsRequiresModelAndCredential(t *testing.T) {
	if _, err := LoadCredentialsFrom(mapLookup(map[string]string{APIKeyEnv: "some-key"})); err == nil || strings.Contains(err.Error(), "some-key") {
		t.Fatal("missing model was not safely rejected")
	}
	if _, err := LoadCredentialsFrom(mapLookup(map[string]string{ModelEnv: "vendor/model"})); err == nil || strings.Contains(err.Error(), "some-key") {
		t.Fatal("missing credential was not rejected")
	}
	if _, err := LoadCredentialsFrom(nil); err == nil {
		t.Fatal("nil environment lookup was accepted")
	}
}

func TestLoadCredentialsFromSecureFileAndRejectsUnsafeFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file credentials are validated through the file owner and DACL; POSIX mode fixtures are not portable")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "openrouter-key")
	if err := os.WriteFile(path, []byte("file-openrouter-secret-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	credentials, err := LoadCredentialsFrom(mapLookup(map[string]string{ModelEnv: "vendor/model", APIKeyFileEnv: path}))
	if err != nil {
		t.Fatal(err)
	}
	if got := credentials.Redact("file-openrouter-secret-123"); got != "[REDACTED]" {
		t.Fatal("file credential was not loaded and redacted")
	}

	insecure := filepath.Join(directory, "insecure-key")
	if err := os.WriteFile(insecure, []byte("insecure-secret-123"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(insecure, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentialsFrom(mapLookup(map[string]string{ModelEnv: "vendor/model", APIKeyFileEnv: insecure})); err == nil || strings.Contains(err.Error(), "insecure-secret-123") {
		t.Fatal("group/world-readable credential file was accepted or leaked")
	}

	symlink := filepath.Join(directory, "symlink-key")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentialsFrom(mapLookup(map[string]string{ModelEnv: "vendor/model", APIKeyFileEnv: symlink})); err == nil {
		t.Fatal("symlink credential file was accepted")
	}
	if _, err := LoadCredentialsFrom(mapLookup(map[string]string{ModelEnv: "vendor/model", APIKeyFileEnv: filepath.Join(directory, "missing")})); err == nil {
		t.Fatal("missing credential file was accepted")
	}
	if _, err := LoadCredentialsFrom(mapLookup(map[string]string{ModelEnv: "vendor/model", APIKeyFileEnv: directory})); err == nil {
		t.Fatal("directory credential file was accepted")
	}
}

func TestLoadCredentialsRejectsOversizedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows private-file fixtures require a custom owner-only DACL")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "large-key")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", maxCredentialBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentialsFrom(mapLookup(map[string]string{ModelEnv: "vendor/model", APIKeyFileEnv: path})); err == nil {
		t.Fatal("oversized credential file was accepted")
	}
}

func testRequest(t *testing.T, endpoint string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
