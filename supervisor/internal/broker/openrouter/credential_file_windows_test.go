//go:build windows

package openrouter

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

func currentUserSIDForTest(t *testing.T) string {
	t.Helper()
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		t.Fatalf("cannot open process token: %v", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatalf("cannot read token user: %v", err)
	}
	sid, err := user.User.Sid.String()
	if err != nil {
		t.Fatalf("cannot format token user SID: %v", err)
	}
	return sid
}

func setTempFileACL(t *testing.T, path string, grants ...string) {
	t.Helper()
	if _, err := exec.LookPath("icacls"); err != nil {
		t.Fatalf("icacls unavailable: %v", err)
	}
	args := []string{path, "/inheritance:r"}
	for _, grant := range grants {
		args = append(args, "/grant:r", grant)
	}
	if out, err := exec.Command("icacls", args...).CombinedOutput(); err != nil {
		t.Fatalf("cannot set test DACL: %v: %s", err, out)
	}
}

func requireOwnerOnlyPositiveControl(t *testing.T, path, sid string) {
	t.Helper()
	setTempFileACL(t, path, "*"+sid+":R")
	content, err := readSecureCredentialFile(path)
	if err != nil {
		t.Fatalf("owner-only positive control rejected (owner mismatch or DACL setup problem): %v", err)
	}
	if string(content) != "test-only-value\n" {
		t.Fatalf("unexpected content %q", content)
	}
}

func writeTempCredentialFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credential.txt")
	if err := os.WriteFile(path, []byte("test-only-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadSecureCredentialFileAcceptsOwnerReadOnlyDACL(t *testing.T) {
	sid := currentUserSIDForTest(t)
	path := writeTempCredentialFile(t)
	requireOwnerOnlyPositiveControl(t, path, sid)
}

func TestReadSecureCredentialFileRejectsAdditionalReadSID(t *testing.T) {
	sid := currentUserSIDForTest(t)
	path := writeTempCredentialFile(t)
	requireOwnerOnlyPositiveControl(t, path, sid)

	setTempFileACL(t, path, "*"+sid+":R", "*S-1-1-0:R")
	if _, err := readSecureCredentialFile(path); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected os.ErrPermission for extra Everyone read grant, got %v", err)
	}
}

func TestReadSecureCredentialFileRejectsExecuteGrant(t *testing.T) {
	sid := currentUserSIDForTest(t)
	path := writeTempCredentialFile(t)
	requireOwnerOnlyPositiveControl(t, path, sid)

	setTempFileACL(t, path, "*"+sid+":RX")
	if _, err := readSecureCredentialFile(path); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected os.ErrPermission for execute grant, got %v", err)
	}
}

func TestCredentialACESIDFitsRejectsTruncatedSID(t *testing.T) {
	const sidOffset = 8
	cases := []struct {
		name     string
		aceSize  uint16
		subCount byte
		want     bool
	}{
		{"ace shorter than SID header", 15, 0, false},
		{"minimal SID with no sub-authorities", 16, 0, true},
		{"sub-authorities exactly fit", 24, 2, true},
		{"sub-authorities one byte short", 23, 2, false},
		{"sub-authority count overclaims", 24, 255, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := make([]uint32, 16)
			ace := (*credentialACEHeader)(unsafe.Pointer(&buf[0]))
			raw := unsafe.Slice((*byte)(unsafe.Pointer(&buf[0])), len(buf)*4)
			ace.AceSize = tc.aceSize
			raw[sidOffset+1] = tc.subCount
			if got := credentialACESIDFits(ace, sidOffset); got != tc.want {
				t.Fatalf("credentialACESIDFits(AceSize=%d, count=%d) = %v, want %v", tc.aceSize, tc.subCount, got, tc.want)
			}
		})
	}
}
