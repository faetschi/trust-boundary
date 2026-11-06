//go:build linux

package productsetup

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"tbound/supervisor/internal/productsession"
)

type recordedRead struct {
	path  string
	limit int64
}

func recordingReader(files map[string]string, failAt string, injected error) (func(string, int64) ([]byte, error), *[]recordedRead) {
	var calls []recordedRead
	return func(path string, limit int64) ([]byte, error) {
		calls = append(calls, recordedRead{path, limit})
		if path == failAt {
			return nil, injected
		}
		return []byte(files[path]), nil
	}, &calls
}

func TestReadCommandRegistrationReadsTheThreeCanonicalFilesInOrder(t *testing.T) {
	files := map[string]string{RegistrationPath: "registration", RegistrationSignaturePath: " c2ln\n", RegistrationPublicKeyPath: "a2V5"}
	read, calls := recordingReader(files, "", nil)
	raw, sig, key, err := ReadCommandRegistration(read)
	if err != nil {
		t.Fatal(err)
	}
	want := []recordedRead{
		{RegistrationPath, int64(productsession.MaxRegistrationBytes)},
		{RegistrationSignaturePath, 1 << 10},
		{RegistrationPublicKeyPath, 1 << 10},
	}
	if !slices.Equal(*calls, want) {
		t.Fatalf("reads = %v, want %v", *calls, want)
	}
	if string(raw) != "registration" || string(sig) != "sig" || string(key) != "key" {
		t.Fatalf("got %q %q %q", raw, sig, key)
	}
}

func TestReadCommandRegistrationReturnsReadErrorsUnchangedAndStops(t *testing.T) {
	injected := errors.New("injected read failure")
	files := map[string]string{RegistrationPath: "r", RegistrationSignaturePath: "c2ln", RegistrationPublicKeyPath: "a2V5"}
	for i, path := range []string{RegistrationPath, RegistrationSignaturePath, RegistrationPublicKeyPath} {
		read, calls := recordingReader(files, path, injected)
		raw, sig, key, err := ReadCommandRegistration(read)
		if err != injected || raw != nil || sig != nil || key != nil {
			t.Fatalf("%s: got %q %q %q %v", path, raw, sig, key, err)
		}
		if len(*calls) != i+1 {
			t.Fatalf("%s: %d reads, want %d (no read after a failure)", path, len(*calls), i+1)
		}
	}
}

func TestReadCommandRegistrationRefusesBadBase64AndTrimsWhitespace(t *testing.T) {
	read, calls := recordingReader(map[string]string{RegistrationPath: "r", RegistrationSignaturePath: "not base64!"}, "", nil)
	_, _, _, err := ReadCommandRegistration(read)
	var policy *CommandPolicyError
	if !errors.As(err, &policy) || policy.Cause == nil || policy.Detail != "input is not standard base64: "+RegistrationSignaturePath {
		t.Fatalf("err = %#v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("%d reads after a bad signature, want 2", len(*calls))
	}
	read, _ = recordingReader(map[string]string{RegistrationPath: "r", RegistrationSignaturePath: "\t c2ln \n", RegistrationPublicKeyPath: "\na2V5\n"}, "", nil)
	if _, sig, key, err := ReadCommandRegistration(read); err != nil || string(sig) != "sig" || string(key) != "key" {
		t.Fatalf("whitespace: %q %q %v", sig, key, err)
	}
}

func commandServiceConfigJSON(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	m := map[string]any{
		"schema_version": ServiceConfigSchema, "registration_path": RegistrationPath,
		"registration_signature_path": RegistrationSignaturePath, "registration_public_key_path": RegistrationPublicKeyPath,
	}
	mutate(m)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDecodeCommandServiceConfig(t *testing.T) {
	cases := []struct {
		name   string
		raw    []byte
		detail string
	}{
		{"valid", commandServiceConfigJSON(t, func(map[string]any) {}), ""},
		{"valid with grace seconds", commandServiceConfigJSON(t, func(m map[string]any) { m["shutdown_grace_seconds"] = 5 }), ""},
		{"duplicate key", []byte(`{"schema_version":"a","schema_version":"b"}`), "service config is not strict JSON"},
		{"unknown field", commandServiceConfigJSON(t, func(m map[string]any) { m["extra"] = 1 }), "service config does not match the product schema"},
		{"wrong schema", commandServiceConfigJSON(t, func(m map[string]any) { m["schema_version"] = "x" }), "service config does not name the canonical registration and key"},
		{"wrong registration path", commandServiceConfigJSON(t, func(m map[string]any) { m["registration_path"] = "/tmp/r" }), "service config does not name the canonical registration and key"},
		{"wrong signature path", commandServiceConfigJSON(t, func(m map[string]any) { m["registration_signature_path"] = "/tmp/s" }), "service config does not name the canonical registration and key"},
		{"wrong key path", commandServiceConfigJSON(t, func(m map[string]any) { m["registration_public_key_path"] = "/tmp/k" }), "service config does not name the canonical registration and key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := DecodeCommandServiceConfig(c.raw)
			if c.detail == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var policy *CommandPolicyError
			if !errors.As(err, &policy) || policy.Detail != c.detail {
				t.Fatalf("err = %v, want detail %q", err, c.detail)
			}
		})
	}
}

func TestRequireCommandRegistration(t *testing.T) {
	registration := productsession.Registration{GitDigest: "d", Projects: []productsession.Project{{ProjectID: "a"}, {ProjectID: "b"}}}
	cases := []struct {
		name    string
		service ServiceConfig
		want    string
	}{
		{"agree", ServiceConfig{GitDigest: "d", Projects: map[string]string{"a": "/x", "b": "/y"}}, ""},
		{"git digest", ServiceConfig{GitDigest: "e", Projects: map[string]string{"a": "/x", "b": "/y"}}, "service git digest differs from the verified registration"},
		{"missing project", ServiceConfig{GitDigest: "d", Projects: map[string]string{"a": "/x"}}, "service projects differ from the verified registration"},
		{"extra project", ServiceConfig{GitDigest: "d", Projects: map[string]string{"a": "/x", "b": "/y", "c": "/z"}}, "service projects differ from the verified registration"},
		{"renamed project", ServiceConfig{GitDigest: "d", Projects: map[string]string{"a": "/x", "c": "/y"}}, "service projects differ from the verified registration: b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.service.RequireCommandRegistration(registration)
			if (err == nil) != (c.want == "") || (err != nil && err.Error() != c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestCommandEffectiveUnitPropertiesAreFreshAndGateValidation(t *testing.T) {
	got := CommandEffectiveUnitProperties()
	got[0] = "changed"
	if CommandEffectiveUnitProperties()[0] != "LoadState" {
		t.Fatal("CommandEffectiveUnitProperties shares its slice")
	}
	if !reflect.DeepEqual(CommandCellEnvironment(), commandCellEnvironment()) {
		t.Fatal("environment differs from the unit's")
	}
}
