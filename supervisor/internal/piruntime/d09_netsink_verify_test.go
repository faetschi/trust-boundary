package piruntime

import (
	"encoding/json"
	"os"
	"strings"
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

func netSinkFixture(t *testing.T, file string) []byte {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestVerifyNetSinkRulesetAcceptsTheListingNft109Prints(t *testing.T) {
	path, level, err := D09NetSinkNftMatch(netSinkCellsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyNetSinkRuleset(netSinkFixture(t, netSinkNft109JSON), path, level); err != nil {
		t.Fatalf("the listing nft 1.0.9 prints for the pinned rule was refused: %v", err)
	}
	// The -j listing carries no level, so the capture must show the loaded rule had the pinned one.
	text := string(netSinkFixture(t, netSinkNft109Text))
	if want := `socket cgroupv2 level 4 "` + path + `"`; strings.Count(text, want) != 2 {
		t.Errorf("the text listing does not hold the pinned cgroup match twice (%s):\n%s", want, text)
	}
}

func TestVerifyNetSinkRulesetRefusesChangesToTheListingNft109Prints(t *testing.T) {
	path, level, _ := D09NetSinkNftMatch(netSinkCellsPath)
	// items[0] is metainfo, then the table, the chain, the accept rule (3) and the drop rule (4).
	rule := func(items []map[string]any, i int) map[string]any { return items[i]["rule"].(map[string]any) }
	exprs := func(items []map[string]any, i int) []any { return rule(items, i)["expr"].([]any) }
	match := func(items []map[string]any, i, j int) map[string]any {
		return exprs(items, i)[j].(map[string]any)["match"].(map[string]any)
	}
	socket := func(items []map[string]any, i int) map[string]any {
		return match(items, i, 0)["left"].(map[string]any)["socket"].(map[string]any)
	}
	cases := map[string]func([]map[string]any) []map[string]any{
		"no drop":    func(items []map[string]any) []map[string]any { return items[:4] },
		"extra rule": func(items []map[string]any) []map[string]any { return append(items, items[4]) },
		"extra accept all": func(items []map[string]any) []map[string]any {
			return append(items, map[string]any{"rule": map[string]any{"family": "inet", "table": D09NetSinkTable, "chain": "output", "expr": []any{map[string]any{"accept": nil}}}})
		},
		"drop before accept": func(items []map[string]any) []map[string]any { items[3], items[4] = items[4], items[3]; return items },
		"other port":         func(items []map[string]any) []map[string]any { match(items, 3, 2)["right"] = 8081; return items },
		"other address":      func(items []map[string]any) []map[string]any { match(items, 3, 1)["right"] = "127.0.0.1"; return items },
		"other cgroup": func(items []map[string]any) []map[string]any {
			match(items, 4, 0)["right"] = "system.slice/other"
			return items
		},
		"cgroup id": func(items []map[string]any) []map[string]any { match(items, 4, 0)["right"] = 4242; return items },
		"drop is accept": func(items []map[string]any) []map[string]any {
			exprs(items, 4)[2] = map[string]any{"accept": nil}
			return items
		},
		"other socket key": func(items []map[string]any) []map[string]any { socket(items, 3)["key"] = "transparent"; return items },
		"other level":      func(items []map[string]any) []map[string]any { socket(items, 3)["level"] = 3; return items },
		"chain policy drop": func(items []map[string]any) []map[string]any {
			items[2]["chain"].(map[string]any)["policy"] = "drop"
			return items
		},
	}
	for name, mutate := range cases {
		var doc struct {
			Items []map[string]any `json:"nftables"`
		}
		if err := json.Unmarshal(netSinkFixture(t, netSinkNft109JSON), &doc); err != nil {
			t.Fatal(err)
		}
		changed, err := json.Marshal(map[string]any{"nftables": mutate(doc.Items)})
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifyNetSinkRuleset(changed, path, level); err == nil {
			t.Errorf("%s: the table was accepted", name)
		}
	}
	if err := VerifyNetSinkRuleset(netSinkFixture(t, netSinkNft109JSON), "system.slice/other", level); err == nil {
		t.Error("a listing for another cgroup path than the pinned one was accepted")
	}
}

// A newer nft that reports the level in -j must still be accepted at the pinned level.
func TestVerifyNetSinkRulesetAcceptsAListingThatReportsThePinnedLevel(t *testing.T) {
	path, level, _ := D09NetSinkNftMatch(netSinkCellsPath)
	listing := strings.ReplaceAll(string(netSinkFixture(t, netSinkNft109JSON)), `{"key": "cgroupv2"}`, `{"key": "cgroupv2", "level": 4}`)
	if err := VerifyNetSinkRuleset([]byte(listing), path, level); err != nil {
		t.Fatalf("a listing with the pinned level was refused: %v", err)
	}
}
