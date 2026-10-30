//go:build windows

package openrouter

import "testing"

func TestCredentialACEHasCompleteSID(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  uint16
		count byte
		want  bool
	}{
		{"missing header", 0, 0, false},
		{"truncated minimum", 15, 0, false},
		{"minimum SID", 16, 0, true},
		{"truncated subauthority", 19, 1, false},
		{"one subauthority", 20, 1, true},
		{"maximum valid count truncated", 75, 15, false},
		{"maximum valid count fits", 76, 15, true},
		{"unsupported count", 80, 16, false},
		{"byte maximum count", 1036, 255, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := credentialACEHasCompleteSID(tc.size, tc.count); got != tc.want {
				t.Fatalf("size=%d count=%d: got %v, want %v", tc.size, tc.count, got, tc.want)
			}
		})
	}
}
