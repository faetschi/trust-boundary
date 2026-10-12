package piruntime

import (
	"strings"
	"testing"

	"tbound/supervisor/internal/delta"
)

func TestValidDigestMatchesCanonicalSHA256Validator(t *testing.T) {
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
			got := validDigest(test.value)
			if got != delta.ValidSHA256(test.value) {
				t.Fatalf("validDigest(%q) = %t, delta.ValidSHA256 = %t", test.value, got, delta.ValidSHA256(test.value))
			}
			if got != test.want {
				t.Fatalf("validDigest(%q) = %t, want %t", test.value, got, test.want)
			}
		})
	}
}
