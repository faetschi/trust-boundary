//go:build linux

package effectoracle

import (
	"encoding/binary"
	"errors"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

// appendInotifyRecord appends one record in the kernel layout: watch
// descriptor, mask, cookie, name length, then the NUL-padded name.
func appendInotifyRecord(buf []byte, wd int32, mask uint32, name string) []byte {
	padded := (len(name) + 1 + 7) &^ 7
	var hdr [inotifyHeaderSize]byte
	binary.NativeEndian.PutUint32(hdr[0:4], uint32(wd))
	binary.NativeEndian.PutUint32(hdr[4:8], mask)
	binary.NativeEndian.PutUint32(hdr[8:12], 0)
	binary.NativeEndian.PutUint32(hdr[12:16], uint32(padded))
	buf = append(buf, hdr[:]...)
	buf = append(buf, name...)
	return append(buf, make([]byte, padded-len(name))...)
}

func TestParseEventsDecodesRecordsAndStripsNamePadding(t *testing.T) {
	var buf []byte
	buf = appendInotifyRecord(buf, 3, syscall.IN_CREATE|syscall.IN_ISDIR, "dir")
	buf = appendInotifyRecord(buf, 3, syscall.IN_MODIFY, "a-much-longer-file-name.txt")
	buf = appendInotifyRecord(buf, -1, syscall.IN_Q_OVERFLOW, "")
	got, err := parseEvents(buf)
	if err != nil {
		t.Fatalf("parseEvents: %v", err)
	}
	want := []rawEvent{
		{wd: 3, mask: syscall.IN_CREATE | syscall.IN_ISDIR, name: "dir"},
		{wd: 3, mask: syscall.IN_MODIFY, name: "a-much-longer-file-name.txt"},
		{wd: -1, mask: syscall.IN_Q_OVERFLOW, name: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseEvents = %+v, want %+v", got, want)
	}
}

func TestParseEventsRejectsTruncatedRecords(t *testing.T) {
	full := appendInotifyRecord(nil, 1, syscall.IN_CREATE, "name")
	cases := []struct {
		name string
		buf  []byte
	}{
		{"header cut short", full[:inotifyHeaderSize-1]},
		{"name runs past the read", full[:len(full)-1]},
	}
	for _, c := range cases {
		if _, err := parseEvents(c.buf); !errors.Is(err, errTruncatedRecord) {
			t.Errorf("%s: parseEvents error = %v, want errTruncatedRecord", c.name, err)
		}
	}
}

// dropNamed removes every record for a name in drop from a batch, as a kernel
// that lost those events would, and returns the rest in the same layout.
func dropNamed(batch []byte, drop map[string]bool) []byte {
	recs, err := parseEvents(batch)
	if err != nil {
		return batch
	}
	var out []byte
	for _, r := range recs {
		if drop[r.name] {
			continue
		}
		out = appendInotifyRecord(out, int32(r.wd), r.mask, r.name)
	}
	return out
}

func TestKernelInotifyWatchRoundTrip(t *testing.T) {
	requireInotify(t)
	k, err := openKernelInotify()
	if err != nil {
		t.Fatalf("openKernelInotify: %v", err)
	}
	defer func() { _ = k.Close() }()
	dir := t.TempDir()
	wd, err := k.AddWatch(dir, watchMask)
	if err != nil {
		t.Fatalf("AddWatch(%s): %v", dir, err)
	}
	if err := k.RmWatch(wd); err != nil {
		t.Fatalf("RmWatch(%d): %v", wd, err)
	}
	if _, err := k.AddWatch(filepath.Join(dir, "missing"), watchMask); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("AddWatch on a missing path: %v, want ENOENT", err)
	}
}
