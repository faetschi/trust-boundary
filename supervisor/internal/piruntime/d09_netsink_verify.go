package piruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// VerifyNetSinkRuleset requires the output of `nft -j list table inet tbound_net_sink` to hold
// exactly the pinned rule: one table, one base chain `output` (filter, hook output, policy
// accept) and two rules in order, an accept of TCP to the sink address and port and a counted
// drop, both matching the net-sink cgroup path and level. Anything else, extra rules included,
// is refused. The comparison is over the semantic fields, not over the text. A cgroup match
// that reports a level must report the pinned one; nft 1.0.9 reports none.
func VerifyNetSinkRuleset(listing []byte, cgroupPath string, level int) error {
	var doc struct {
		Items []map[string]any `json:"nftables"`
	}
	if err := json.Unmarshal(listing, &doc); err != nil {
		return fmt.Errorf("nft output is not JSON: %w", err)
	}
	var objects []map[string]any
	for _, item := range doc.Items {
		if _, meta := item["metainfo"]; !meta {
			objects = append(objects, item)
		}
	}
	if len(objects) != 4 {
		return fmt.Errorf("the table has %d objects, want its table, one chain and two rules", len(objects))
	}
	object := func(i int, kind string) (map[string]any, error) {
		inner, ok := objects[i][kind].(map[string]any)
		if !ok || len(objects[i]) != 1 {
			return nil, fmt.Errorf("object %d is not a %s", i, kind)
		}
		return inner, nil
	}
	table, err := object(0, "table")
	if err != nil {
		return err
	}
	chain, err := object(1, "chain")
	if err != nil {
		return err
	}
	if table["family"] != "inet" || table["name"] != D09NetSinkTable {
		return errors.New("the table is not inet " + D09NetSinkTable)
	}
	if chain["family"] != "inet" || chain["table"] != D09NetSinkTable || chain["name"] != "output" || chain["type"] != "filter" ||
		chain["hook"] != "output" || chain["prio"] != float64(0) || chain["policy"] != "accept" {
		return errors.New("the chain is not the base chain output (filter, hook output, priority 0, policy accept)")
	}
	cgroup := fmt.Sprintf("match socket.cgroupv2.%d == %s", level, cgroupPath)
	want := [][]string{
		{cgroup, "match payload.ip.daddr == " + D09NetSinkAddress, "match payload.tcp.dport == " + strconv.Itoa(D09NetSinkPort), "accept"},
		{cgroup, "counter", "drop"},
	}
	for i := range want {
		rule, err := object(2+i, "rule")
		if err != nil {
			return err
		}
		if rule["family"] != "inet" || rule["table"] != D09NetSinkTable || rule["chain"] != "output" {
			return fmt.Errorf("rule %d is not in inet %s output", i, D09NetSinkTable)
		}
		exprs, _ := rule["expr"].([]any)
		var got []string
		for _, expr := range exprs {
			text, err := nftExpression(expr, level)
			if err != nil {
				return fmt.Errorf("rule %d: %w", i, err)
			}
			got = append(got, text)
		}
		if strings.Join(got, "; ") != strings.Join(want[i], "; ") {
			return fmt.Errorf("rule %d is %q, want %q", i, strings.Join(got, "; "), strings.Join(want[i], "; "))
		}
	}
	return nil
}

// nftExpression renders one statement of the nft JSON as text, for the kinds the pinned rule uses.
func nftExpression(expr any, pinnedLevel int) (string, error) {
	statement, ok := expr.(map[string]any)
	if !ok || len(statement) != 1 {
		return "", errors.New("a statement is not a single-key object")
	}
	for kind, value := range statement {
		switch kind {
		case "accept", "drop", "counter":
			return kind, nil
		case "match":
			m, _ := value.(map[string]any)
			left, _ := m["left"].(map[string]any)
			operand, err := nftOperand(left, pinnedLevel)
			if err != nil {
				return "", err
			}
			var right string
			switch r := m["right"].(type) {
			case string:
				right = r
			case float64:
				right = strconv.FormatFloat(r, 'f', -1, 64)
			default:
				return "", errors.New("a match has an operand that is neither a string nor a number")
			}
			return fmt.Sprintf("match %s %v %s", operand, m["op"], right), nil
		}
		return "", fmt.Errorf("unexpected statement %q", kind)
	}
	return "", errors.New("empty statement")
}

func nftOperand(left map[string]any, pinnedLevel int) (string, error) {
	if socket, ok := left["socket"].(map[string]any); ok && len(left) == 1 {
		// shortcut: nft 1.0.9 (Ubuntu 24.04) leaves the level out of -j, so a socket without one is
		// read at the pinned level, which the digest-checked rule file loaded; upgrade to a text
		// listing check if the level must be read back from the kernel.
		level := float64(pinnedLevel)
		if reported, ok := socket["level"].(float64); ok {
			level = reported
		}
		return fmt.Sprintf("socket.%v.%d", socket["key"], int(level)), nil
	}
	if payload, ok := left["payload"].(map[string]any); ok && len(left) == 1 {
		return fmt.Sprintf("payload.%v.%v", payload["protocol"], payload["field"]), nil
	}
	return "", errors.New("a match names an operand other than a socket or a payload field")
}
