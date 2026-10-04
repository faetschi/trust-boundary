//go:build linux

package sessionrepo

import (
	"errors"
	"strings"
	"testing"

	"tbound/supervisor/internal/delta"
)

func TestCommandExecutionContextDigestRequiresCanonicalTreeDigest(t *testing.T) {
	viewID := "view-" + strings.Repeat("0", 48)
	leaseID := "lease-context-digest"
	validTreeDigest := delta.TreeDigestProfile + strings.Repeat("0", 64)
	if _, err := commandExecutionContextDigest(viewID, validTreeDigest, leaseID); err != nil {
		t.Fatalf("valid tree digest rejected: %v", err)
	}

	invalidDigests := []struct {
		name   string
		digest string
	}{
		{name: "plain SHA-256", digest: "sha256:" + strings.Repeat("0", 64)},
		{name: "unknown profile", digest: "unknown:" + strings.Repeat("0", 64)},
		{name: "uppercase hex", digest: delta.TreeDigestProfile + strings.Repeat("A", 64)},
		{name: "short hex", digest: delta.TreeDigestProfile + strings.Repeat("0", 63)},
	}
	for _, test := range invalidDigests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := commandExecutionContextDigest(viewID, test.digest, leaseID); !errors.Is(err, ErrInvalidOptions) {
				t.Fatalf("invalid tree digest returned %v, want %v", err, ErrInvalidOptions)
			}
		})
	}
}
