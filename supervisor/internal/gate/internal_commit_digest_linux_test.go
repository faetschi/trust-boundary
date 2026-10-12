//go:build linux

package gate

import (
	"strings"
	"testing"

	"tbound/supervisor/internal/delta"
)

func TestInternalDigestMatchesCanonicalSHA256Validator(t *testing.T) {
	valid := "sha256:" + strings.Repeat("a", 64)
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "lowercase hex", value: valid, want: true},
		{name: "mixed lowercase hex", value: "sha256:" + strings.Repeat("0f", 32), want: true},
		{name: "uppercase hex", value: "sha256:" + strings.Repeat("A", 64)},
		{name: "uppercase prefix", value: "SHA256:" + strings.Repeat("a", 64)},
		{name: "short hex", value: "sha256:" + strings.Repeat("a", 63)},
		{name: "non-hex", value: "sha256:" + strings.Repeat("a", 63) + "g"},
		{name: "trailing byte", value: valid + "0"},
		{name: "wrong algorithm", value: "sha512:" + strings.Repeat("a", 64)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := internalDigest(test.value)
			if got != delta.ValidSHA256(test.value) {
				t.Fatalf("internalDigest(%q) = %t, delta.ValidSHA256 = %t", test.value, got, delta.ValidSHA256(test.value))
			}
			if got != test.want {
				t.Fatalf("internalDigest(%q) = %t, want %t", test.value, got, test.want)
			}
		})
	}
}

func TestInternalTreeDigestRetainsTaggedTreeAndRawSHA256Forms(t *testing.T) {
	encoded := strings.Repeat("a", 64)
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "raw sha256", value: "sha256:" + encoded, want: true},
		{name: "tagged tree digest", value: delta.TreeDigestProfile + encoded, want: true},
		{name: "tagged uppercase hex", value: delta.TreeDigestProfile + strings.ToUpper(encoded)},
		{name: "tagged short hex", value: delta.TreeDigestProfile + encoded[:63]},
		{name: "unknown tree profile", value: "tbound-tree-jcs-rfc8785/v2:sha256:" + encoded},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := internalTreeDigest(test.value); got != test.want {
				t.Fatalf("internalTreeDigest(%q) = %t, want %t", test.value, got, test.want)
			}
		})
	}
}
