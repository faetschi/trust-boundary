//go:build linux

package piruntime

import (
	"errors"
	"testing"

	"tbound/supervisor/internal/productsetup"
)

// A productsetup policy refusal must wrap exactly as a refusal made by productD09Refused does: [sentinel] for a cause-free
// refusal and [sentinel, cause] for one with a cause, with the same message.
func TestProductD09PolicyRefusalKeepsTheRefusalShape(t *testing.T) {
	cause := errors.New("cause")
	for _, c := range []struct {
		name   string
		policy error
		cause  error
		detail string
	}{
		{"cause-free", &productsetup.CommandPolicyError{Detail: "detail"}, nil, "detail"},
		{"with cause", &productsetup.CommandPolicyError{Detail: "detail", Cause: cause}, cause, "detail"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, want := productD09RefusedPolicy(c.policy), productD09Refused(c.cause, c.detail)
			if got.Error() != want.Error() {
				t.Fatalf("message %q, want %q", got, want)
			}
			if !errors.Is(got, ErrProductD09HostRefused) || errors.Is(got, cause) != (c.cause != nil) {
				t.Fatalf("wrong identities: %v", got)
			}
			_, gotMulti := got.(interface{ Unwrap() []error })
			_, wantMulti := want.(interface{ Unwrap() []error })
			if gotMulti != wantMulti || (errors.Unwrap(got) == nil) != (errors.Unwrap(want) == nil) {
				t.Fatalf("unwrap shape differs: got multi=%v want multi=%v", gotMulti, wantMulti)
			}
			if c.cause == nil && errors.Unwrap(got) != ErrProductD09HostRefused {
				t.Fatalf("cause-free refusal must unwrap to the sentinel, got %v", errors.Unwrap(got))
			}
		})
	}
	read := errors.New("read refusal")
	if productD09RefusedPolicy(read) != read {
		t.Fatal("a non-policy error must pass through unchanged")
	}
}
