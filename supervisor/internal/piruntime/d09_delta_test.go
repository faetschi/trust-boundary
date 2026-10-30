package piruntime

import (
	"errors"
	"testing"
)

func TestParseD09CellDeltaIsTheClosedCellSet(t *testing.T) {
	none, err := ParseD09CellDelta("")
	if err != nil || none.Relaxed() || none.Name() != "" || none.NetSink() || none.NoReap() {
		t.Fatalf("the empty name must be the unrelaxed cell: %+v %v", none, err)
	}
	for name, check := range map[string]func(D09CellDelta) bool{
		D09CellDeltaNetSink: D09CellDelta.NetSink, D09CellDeltaNoReap: D09CellDelta.NoReap,
	} {
		delta, err := ParseD09CellDelta(name)
		if err != nil || !delta.Relaxed() || delta.Name() != name || !check(delta) {
			t.Errorf("%s: %+v %v", name, delta, err)
		}
	}
	// admit-all/v1 relaxes admission, not the cell; none is the absent member of the manifest. Neither
	// names a cell delta, and no other string may select a relaxed cell.
	for _, name := range []string{"none", "admit-all/v1", "net-sink", "net-sink/v2", "no-reap/v2", "NET-SINK/V1", " net-sink/v1", "net-sink/v1 "} {
		if delta, err := ParseD09CellDelta(name); !errors.Is(err, ErrD09CellDeltaUnknown) || delta.Relaxed() {
			t.Errorf("%q selected a cell delta: %+v %v", name, delta, err)
		}
	}
}

func TestD09CellDeltaPolicyIsExactlyTheRegisteredRelaxation(t *testing.T) {
	none, _ := ParseD09CellDelta("")
	netSink, _ := ParseD09CellDelta(D09CellDeltaNetSink)
	noReap, _ := ParseD09CellDelta(D09CellDeltaNoReap)

	if none.NetworkMode() != "none" || noReap.NetworkMode() != "none" || netSink.NetworkMode() != "host" {
		t.Errorf("network modes: none=%s no-reap=%s net-sink=%s", none.NetworkMode(), noReap.NetworkMode(), netSink.NetworkMode())
	}
	if got := netSink.ConnectPorts(); len(got) != 1 || got[0] != 8080 || netSink.ConnectPortsText() != "8080" {
		t.Errorf("net-sink must allow exactly TCP port 8080: %v", got)
	}
	if D09NetSinkAddress != "127.0.33.1" || D09NetSinkPort != 8080 {
		t.Errorf("the registered sink is 127.0.33.1:8080, got %s:%d", D09NetSinkAddress, D09NetSinkPort)
	}
	for name, delta := range map[string]D09CellDelta{"none": none, "no-reap": noReap} {
		if len(delta.ConnectPorts()) != 0 || delta.ConnectPortsText() != "" || delta.ScopedAbstractUnix() || !delta.ExpectNoExternalRoutes() {
			t.Errorf("%s must not relax the network: ports=%v", name, delta.ConnectPorts())
		}
		if delta.NamespaceProfile() != "user,mount,pid,ipc,uts,network-none/v1" {
			t.Errorf("%s namespace profile = %s", name, delta.NamespaceProfile())
		}
	}
	if netSink.ExpectNoExternalRoutes() || !netSink.ScopedAbstractUnix() || netSink.NamespaceProfile() == none.NamespaceProfile() {
		t.Errorf("net-sink shares the host network namespace and scopes abstract unix sockets: %+v", netSink)
	}
	if none.NoReap() || netSink.NoReap() || !noReap.NoReap() || noReap.NetSink() {
		t.Error("the two deltas must be independent")
	}
}

func TestD09CellDeltaTemplateDigestIsADistinctDerivedIdentity(t *testing.T) {
	base := DigestBytes([]byte("signed base template"))
	none, _ := ParseD09CellDelta("")
	netSink, _ := ParseD09CellDelta(D09CellDeltaNetSink)
	noReap, _ := ParseD09CellDelta(D09CellDeltaNoReap)

	noneDigest, err := none.TemplateDigest(base)
	if err != nil || noneDigest != base {
		t.Fatalf("the unrelaxed cell must keep the signed template digest: %s %v", noneDigest, err)
	}
	netDigest, err := netSink.TemplateDigest(base)
	if err != nil || !validDigest(netDigest) {
		t.Fatalf("net-sink digest: %s %v", netDigest, err)
	}
	reapDigest, err := noReap.TemplateDigest(base)
	if err != nil || !validDigest(reapDigest) {
		t.Fatalf("no-reap digest: %s %v", reapDigest, err)
	}
	if netDigest == base || reapDigest == base || netDigest == reapDigest {
		t.Fatal("each delta must have a template digest of its own")
	}
	again, _ := netSink.TemplateDigest(base)
	if again != netDigest {
		t.Fatal("the delta template digest is not deterministic")
	}
	other, _ := netSink.TemplateDigest(DigestBytes([]byte("another signed base template")))
	if other == netDigest {
		t.Fatal("the delta template digest does not depend on the signed base template")
	}
	for _, bad := range []string{"", "sha256:short", "not-a-digest"} {
		if _, err := netSink.TemplateDigest(bad); err == nil {
			t.Errorf("base digest %q was accepted", bad)
		}
	}
}
