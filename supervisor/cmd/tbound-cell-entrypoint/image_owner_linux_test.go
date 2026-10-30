//go:build linux

package main

import (
	"testing"
)

// The real guest cell runs rootless podman with a single own-uid mapping and no
// subuid range, so /proc/self/uid_map in the cell is "1000 0 1" and an image
// file written by the image root (outside uid 0) shows up inside the cell owned
// by uid 1000. The inside uid whose outside id is 0 is the image root; a literal
// uid 0 test refused every image file on that host.
func TestParseUIDMapRootUID(t *testing.T) {
	for name, test := range map[string]struct {
		raw  string
		want uint32
		err  bool
	}{
		"rootless single own-uid mapping (the real guest cell)": {raw: "1000 0 1\n", want: 1000},
		"rootful mapping is unchanged":                          {raw: "0 0 4294967295\n", want: 0},
		"outside uid 0 is unmapped":                             {raw: "0 1000 1\n", err: true},
		"several ranges, outside 0 in the second line":          {raw: "0 1000 1\n1000 0 1\n", want: 1000},
		"several ranges, outside 0 in the first line":           {raw: "0 0 1\n1000 1000 1\n", want: 0},
		"outside uid 0 claimed twice":                           {raw: "0 0 1\n1000 0 1\n", err: true},
		"empty map":                                             {raw: "\n", err: true},
		"two fields":                                            {raw: "1000 0\n", err: true},
		"non-decimal field":                                     {raw: "1000 0 x\n", err: true},
		"negative field":                                        {raw: "-1000 0 1\n", err: true},
		"field beyond the id space":                             {raw: "4294967296 0 1\n", err: true},
		"zero length":                                           {raw: "1000 0 0\n", err: true},
		"inside range overflows the id space":                   {raw: "4294967295 0 2\n", err: true},
		"outside range overflows the id space":                  {raw: "1000 4294967295 2\n", err: true},
		"blank line inside the map":                             {raw: "1000 0 1\n\n0 1 1\n", err: true},
	} {
		got, err := parseUIDMapRootUID(test.raw)
		if test.err {
			if err == nil {
				t.Errorf("%s: %q was accepted as the image root uid %d", name, test.raw, got)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Errorf("%s: %q = %d, %v; want %d", name, test.raw, got, err, test.want)
		}
	}
}

// The ownership decision the command cell makes about the executable it is
// about to run, over the uid_map the cell measured and registered: the owner must be the inside
// uid that this namespace maps to outside uid 0. An unmapped outside 0 is refused, so the check
// fails closed instead of falling back to uid 0.
func TestRequireImageRootOwnedAcceptsOnlyTheMappedRoot(t *testing.T) {
	rootless, rootful, unmapped := "1000 0 1\n", "0 0 4294967295\n", "0 1000 1\n"
	for name, test := range map[string]struct {
		owner  uint32
		uidMap string
		accept bool
	}{
		"rootless cell accepts its mapped root":    {owner: 1000, uidMap: rootless, accept: true},
		"rootless cell refuses uid 0":              {owner: 0, uidMap: rootless},
		"rootless cell refuses a neighbouring uid": {owner: 1001, uidMap: rootless},
		"rootless cell refuses the overflow uid":   {owner: 65534, uidMap: rootless},
		"rootful keeps accepting uid 0":            {owner: 0, uidMap: rootful, accept: true},
		"rootful refuses the cell uid":             {owner: 1000, uidMap: rootful},
		"unmapped outside 0 refuses uid 0":         {owner: 0, uidMap: unmapped},
		"unmapped outside 0 refuses the cell uid":  {owner: 1000, uidMap: unmapped},
		"empty map refuses":                        {owner: 0},
		"malformed map refuses":                    {owner: 1000, uidMap: "garbage\n"},
	} {
		err := requireImageRootOwned(test.owner, test.uidMap)
		if test.accept && err != nil {
			t.Errorf("%s: uid %d was refused: %v", name, test.owner, err)
		}
		if !test.accept && err == nil {
			t.Errorf("%s: uid %d was accepted", name, test.owner)
		}
	}
}

// TestRequireImageRootOwnedUsesTheMeasuredMap: the map that was measured at start and compared
// with the registered digest decides, not a second read of the process's own map.
func TestRequireImageRootOwnedUsesTheMeasuredMap(t *testing.T) {
	if err := requireImageRootOwned(0, "0 0 4294967295\n"); err != nil {
		t.Fatalf("the measured rootful map was not used: %v", err)
	}
	if err := requireImageRootOwned(0, "1000 0 1\n"); err == nil {
		t.Fatal("uid 0 was accepted against a rootless measured map")
	}
	if err := requireImageRootOwned(1000, "1000 0 1\n"); err != nil {
		t.Fatalf("the measured rootless map was not used: %v", err)
	}
}
