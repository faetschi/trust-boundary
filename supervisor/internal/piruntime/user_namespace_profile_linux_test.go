//go:build linux

package piruntime

import "testing"

func TestD09UserNamespaceMapsSeparateCellAndServiceIDs(t *testing.T) {
	const (
		cellUID    = 731
		cellGID    = 732
		serviceUID = 1701
		serviceGID = 1702
	)
	digest := DigestD09UserNamespaceMapping(cellUID, cellGID, serviceUID, serviceGID)
	if digest == "" || digest == DigestD09UserNamespaceMapping(cellUID, cellGID, cellUID, cellGID) {
		t.Fatal("map digest did not bind distinct cell and service IDs")
	}
	insideUID, insideGID := []byte("731 0 1\n"), []byte("732 0 1\n")
	if err := VerifyD09InsideIDMaps(D09UserNamespaceMappingProfile, digest,
		cellUID, cellGID, serviceUID, serviceGID, insideUID, insideGID); err != nil {
		t.Fatalf("verify inside map: %v", err)
	}
	if err := VerifyD09InsideIDMaps(D09UserNamespaceMappingProfile, digest,
		cellUID, cellGID, cellUID, cellGID, insideUID, insideGID); err == nil {
		t.Fatal("inside map accepted a digest linked to another host service identity")
	}
	if err := VerifyD09HostIDMaps(D09UserNamespaceMappingProfile, digest,
		cellUID, cellGID, serviceUID, serviceGID, []byte("731 1701 1\n"), []byte("732 1702 1\n")); err != nil {
		t.Fatalf("verify host-visible map: %v", err)
	}
	if err := VerifyD09HostIDMaps(D09UserNamespaceMappingProfile, digest,
		cellUID, cellGID, serviceUID, serviceGID, []byte("731 731 1\n"), []byte("732 732 1\n")); err == nil {
		t.Fatal("host-visible map accepted cell IDs in place of the service IDs")
	}
}

func TestD09InsideIDMapsRequireExactSingleUIDAndGIDRange(t *testing.T) {
	const (
		cellUID    = 731
		cellGID    = 732
		serviceUID = 1701
		serviceGID = 1702
	)
	digest := DigestD09UserNamespaceMapping(cellUID, cellGID, serviceUID, serviceGID)
	validUID, validGID := []byte("731 0 1\n"), []byte("732 0 1\n")
	tests := []struct {
		name   string
		uidMap []byte
		gidMap []byte
	}{
		{name: "extra UID range", uidMap: []byte("731 0 1\n732 0 1\n"), gidMap: validGID},
		{name: "extra GID range", uidMap: validUID, gidMap: []byte("732 0 1\n733 0 1\n")},
		{name: "wrong UID start", uidMap: []byte("730 0 1\n"), gidMap: validGID},
		{name: "wrong UID parent", uidMap: []byte("731 1 1\n"), gidMap: validGID},
		{name: "wrong UID count", uidMap: []byte("731 0 2\n"), gidMap: validGID},
		{name: "wrong GID start", uidMap: validUID, gidMap: []byte("733 0 1\n")},
		{name: "wrong GID parent", uidMap: validUID, gidMap: []byte("732 1 1\n")},
		{name: "wrong GID count", uidMap: validUID, gidMap: []byte("732 0 2\n")},
		{name: "malformed UID", uidMap: []byte("731 nope 1\n"), gidMap: validGID},
		{name: "overflow UID", uidMap: []byte("731 4294967296 1\n"), gidMap: validGID},
		{name: "malformed GID", uidMap: validUID, gidMap: []byte("732 0\n")},
		{name: "overflow GID", uidMap: validUID, gidMap: []byte("732 0 4294967296\n")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := VerifyD09InsideIDMaps(D09UserNamespaceMappingProfile, digest,
				cellUID, cellGID, serviceUID, serviceGID, test.uidMap, test.gidMap); err == nil {
				t.Fatalf("accepted non-exact inside maps uid=%q gid=%q", test.uidMap, test.gidMap)
			}
		})
	}
}

func TestD09HostIDMapsRequireExactSingleUIDAndGIDRange(t *testing.T) {
	const (
		cellUID    = 731
		cellGID    = 732
		serviceUID = 1701
		serviceGID = 1702
	)
	digest := DigestD09UserNamespaceMapping(cellUID, cellGID, serviceUID, serviceGID)
	validUID, validGID := []byte("731 1701 1\n"), []byte("732 1702 1\n")
	tests := []struct {
		name   string
		uidMap []byte
		gidMap []byte
	}{
		{name: "extra UID range", uidMap: []byte("731 1701 1\n732 1702 1\n"), gidMap: validGID},
		{name: "extra GID range", uidMap: validUID, gidMap: []byte("732 1702 1\n733 1703 1\n")},
		{name: "wrong UID start", uidMap: []byte("730 1701 1\n"), gidMap: validGID},
		{name: "wrong UID host", uidMap: []byte("731 1703 1\n"), gidMap: validGID},
		{name: "wrong UID count", uidMap: []byte("731 1701 2\n"), gidMap: validGID},
		{name: "wrong GID start", uidMap: validUID, gidMap: []byte("733 1702 1\n")},
		{name: "wrong GID host", uidMap: validUID, gidMap: []byte("732 1703 1\n")},
		{name: "wrong GID count", uidMap: validUID, gidMap: []byte("732 1702 2\n")},
		{name: "malformed UID", uidMap: []byte("731 1701\n"), gidMap: validGID},
		{name: "overflow UID", uidMap: []byte("731 4294967296 1\n"), gidMap: validGID},
		{name: "malformed GID", uidMap: validUID, gidMap: []byte("732 1702 invalid\n")},
		{name: "overflow GID", uidMap: validUID, gidMap: []byte("732 1702 4294967296\n")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := VerifyD09HostIDMaps(D09UserNamespaceMappingProfile, digest,
				cellUID, cellGID, serviceUID, serviceGID, test.uidMap, test.gidMap); err == nil {
				t.Fatalf("accepted non-exact host-visible maps uid=%q gid=%q", test.uidMap, test.gidMap)
			}
		})
	}
}
