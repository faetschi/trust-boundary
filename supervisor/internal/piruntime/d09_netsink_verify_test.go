package piruntime

import (
	"os"
	"testing"
)

// The fixtures are the output of nftables v1.0.9 (the Ubuntu 24.04 guest package, 1.0.9-1ubuntu0.1)
// for the pinned rule file, loaded into a private network namespace with the cgroup of the rule
// present. testdata/nft/capture-netsink-listing.sh regenerates them. The .json file is verbatim
// `nft -j list table inet tbound_net_sink`; the .txt file is `nft list table inet tbound_net_sink`.
const (
	netSinkNft109JSON = "testdata/nft/netsink-nft-1.0.9.json"
	netSinkNft109Text = "testdata/nft/netsink-nft-1.0.9.txt"
	netSinkCellsPath  = "/sys/fs/cgroup/system.slice/tbound-pi.service/cells"
)

func TestVerifyNetSinkRulesetAcceptsTheListingNft109Prints(t *testing.T) {
	path, level, err := D09NetSinkNftMatch(netSinkCellsPath)
	if err != nil {
		t.Fatal(err)
	}
	listing, err := os.ReadFile(netSinkNft109JSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyNetSinkRuleset(listing, path, level); err != nil {
		t.Fatalf("the listing nft 1.0.9 prints for the pinned rule was refused: %v", err)
	}
}
