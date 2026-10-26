// Package panelaudit is the reading side of the three files of a tbound-panel run that a manifest's
// construct_review binds: receipt.json, the advisor-signed audit.json and stats.json (D35 amendment
// 2026-10-24). tbound-panel writes them with the schema versions and advisor fields defined here,
// and `tbound-profile corpus-digest` calls Verify before it prints a digest.
package panelaudit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/evalcorpus"
)

const (
	ReceiptSchema = "tbound-panel-receipt/v3"
	AuditSchema   = "tbound-panel-audit/v3"
	// MaxFileBytes bounds each file; the importer reads at most this much.
	MaxFileBytes = 16 << 20
)

// Advisor is the advisor's entry for one case of the audit list. Decision is accept, reject or
// revise; only accept can back an accepted manifest.
type Advisor struct {
	Decision   string `json:"decision"`
	Resolution string `json:"resolution"`
	Identity   string `json:"identity"`
	SignedAt   string `json:"signed_at"`
}

// Receipt is the part of receipt.json that Verify reads.
type Receipt struct {
	Provider      string    `json:"provider"`
	LiveRun       bool      `json:"live_run"`
	Models        []Model   `json:"models"`
	CallsRecorded int       `json:"calls_recorded"`
	Outcomes      []Outcome `json:"rating_outcomes"`
	Seeds         struct {
		AuditSample      string `json:"audit_sample"`
		BlindIDsAndOrder uint64 `json:"blind_ids_and_order"`
		Bootstrap        uint64 `json:"bootstrap"`
	} `json:"seeds"`
	Schema        string `json:"schema"`
	DryRun        bool   `json:"dry_run"`
	ContentDigest string `json:"content_digest"`
	Cases         int    `json:"cases"`
	CompleteCases int    `json:"complete_cases"`
}

// Audit is the part of audit.json that Verify reads.
type Audit struct {
	Provider      string      `json:"provider"`
	LiveRun       bool        `json:"live_run"`
	Selection     Selection   `json:"selection"`
	Schema        string      `json:"schema"`
	DryRun        bool        `json:"dry_run"`
	ContentDigest string      `json:"content_digest"`
	Cases         []AuditCase `json:"cases"`
}

// AuditCase is one case on the audit list.
type AuditCase struct {
	Reasons []string `json:"reasons"`
	CaseID  string   `json:"case_id"`
	Advisor Advisor  `json:"advisor_signoff"`
}

// Digest is "sha256:" followed by the lowercase hex SHA-256 of raw.
func Digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// parse bounds, strictly checks and decodes one file into v. Unknown members are tolerated: the
// manifest pins the file's bytes, so only the members read here matter.
func parse(name string, raw []byte, v any) error {
	if len(raw) > MaxFileBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, MaxFileBytes)
	}
	if err := protocol.ValidateEvidenceJSON(raw); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err := required(raw, reflect.TypeOf(v).Elem(), name); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// Verify checks the receipt, audit and stats file bytes against the manifest m, which holds the advisor's
// sign-offs and the construct_review block. It returns nil only when:
//   - the SHA-256 of each file's bytes is the digest in m.ConstructReview;
//   - all files have the current schema and record the evalcorpus.ContentDigest of m, so they rate
//     the content the manifest has now;
//   - no file is from a dry run, and the receipt covers every case of m with a complete rating
//     (a case without every rating blocks every agreement gate);
//   - the cases of the audit list are exactly the cases of m marked audited, each once, each with a
//     resolution in m;
//   - every listed case carries a complete advisor entry that accepts it.
//
// Every phase-2 item must carry the panel writer's pass verdict in the pinned stats.
func Verify(m evalcorpus.Manifest, receiptRaw, auditRaw, statsRaw []byte) error {
	cr := m.ConstructReview
	if got := Digest(receiptRaw); got != cr.PanelReceiptDigest {
		return fmt.Errorf("panel receipt digest %s is not construct_review.panel_receipt_digest %s", got, cr.PanelReceiptDigest)
	}
	if got := Digest(auditRaw); got != cr.PanelAuditDigest {
		return fmt.Errorf("panel audit digest %s is not construct_review.panel_audit_digest %s", got, cr.PanelAuditDigest)
	}
	if got := Digest(statsRaw); got != cr.PanelStatsDigest {
		return fmt.Errorf("panel stats digest %s is not construct_review.panel_stats_digest %s", got, cr.PanelStatsDigest)
	}
	var stats Stats
	if err := parse("panel stats", statsRaw, &stats); err != nil {
		return err
	}
	if stats.Schema != StatsSchema {
		return fmt.Errorf("panel stats schema %q; want %q", stats.Schema, StatsSchema)
	}
	if stats.DryRun || !stats.LiveRun || stats.Provider != "openrouter" {
		return errors.New("panel stats is a dry run")
	}
	contentStats, err := evalcorpus.ContentDigest(m)
	if err != nil {
		return err
	}
	if stats.ContentDigest != contentStats {
		return errors.New("panel stats content_digest differs from manifest")
	}
	if stats.Cases != len(m.Cases) || stats.CompleteCases != stats.Cases || len(stats.Incomplete) != 0 || len(stats.Missing) != 0 {
		return errors.New("panel stats has incomplete ratings; every gate is blocked")
	}
	for _, item := range Checklist {
		st := stats.Items[item.Key]
		expected := Gate(st.Kappa, st.Agreement, st.KappaDefined)
		if st.Kappa < -1 || st.Kappa > 1 || st.Agreement < 0 || st.Agreement > 1 || st.Subjects <= 0 || st.Roots <= 0 {
			return fmt.Errorf("panel stats item %s invalid numeric fields", item.Key)
		}
		if st.Gate != expected {
			return fmt.Errorf("panel stats item %s gate %q contradicts computed gate %q", item.Key, st.Gate, expected)
		}
		if st.Gate != GatePass {
			return fmt.Errorf("panel stats item %s gate %q does not pass", item.Key, stats.Items[item.Key].Gate)
		}
	}
	var receipt Receipt
	if err := parse("panel receipt", receiptRaw, &receipt); err != nil {
		return err
	}
	var audit Audit
	if err := parse("panel audit", auditRaw, &audit); err != nil {
		return err
	}
	if receipt.Schema != ReceiptSchema {
		return fmt.Errorf("panel receipt schema is %q; want %q", receipt.Schema, ReceiptSchema)
	}
	if audit.Schema != AuditSchema {
		return fmt.Errorf("panel audit schema is %q; want %q", audit.Schema, AuditSchema)
	}
	if receipt.DryRun || audit.DryRun || !receipt.LiveRun || !audit.LiveRun || receipt.Provider != "openrouter" || audit.Provider != "openrouter" {
		return errors.New("the panel run is a dry run: its ratings are canned fake data")
	}
	content, err := evalcorpus.ContentDigest(m)
	if err != nil {
		return err
	}
	if receipt.ContentDigest != content {
		return fmt.Errorf("panel receipt content_digest %s is not the content digest %s of the manifest", receipt.ContentDigest, content)
	}
	if audit.ContentDigest != content {
		return fmt.Errorf("panel audit content_digest %s is not the content digest %s of the manifest", audit.ContentDigest, content)
	}
	if receipt.Cases != len(m.Cases) || receipt.CompleteCases != receipt.Cases {
		return fmt.Errorf("panel receipt rates %d of %d cases completely; the manifest has %d cases and a missing rating blocks every agreement gate",
			receipt.CompleteCases, receipt.Cases, len(m.Cases))
	}
	if err := verifyCoverage(m, receipt); err != nil {
		return err
	}
	if err := verifyAudited(m, audit); err != nil {
		return err
	}
	return verifySelection(m, audit, receipt)
}

func verifyAudited(m evalcorpus.Manifest, audit Audit) error {
	byID := make(map[string]evalcorpus.Signoff, len(m.Cases))
	for _, c := range m.Cases {
		byID[c.CaseID] = c.Signoff
	}
	listed := make(map[string]bool, len(audit.Cases))
	for _, ac := range audit.Cases {
		so, ok := byID[ac.CaseID]
		switch {
		case !ok:
			return fmt.Errorf("panel audit lists case %q, which the manifest does not have", ac.CaseID)
		case listed[ac.CaseID]:
			return fmt.Errorf("panel audit lists case %q twice", ac.CaseID)
		case !so.Audited || strings.TrimSpace(so.Resolution) == "":
			return fmt.Errorf("case %q is on the panel audit list but its signoff is not audited with a resolution", ac.CaseID)
		}
		if err := ac.Advisor.complete(); err != nil {
			return fmt.Errorf("panel audit case %q: %w", ac.CaseID, err)
		}
		if !evalcorpus.SameIdentity(ac.Advisor.Identity, so.Reviewer) {
			return fmt.Errorf("panel audit case %q advisor identity %q differs from signoff reviewer %q", ac.CaseID, ac.Advisor.Identity, so.Reviewer)
		}
		listed[ac.CaseID] = true
	}
	for _, c := range m.Cases {
		if c.Signoff.Audited && !listed[c.CaseID] {
			return fmt.Errorf("case %q is marked audited but is not on the panel audit list", c.CaseID)
		}
	}
	return nil
}

// complete reports the first advisor field that is blank, still a TODO placeholder, or a decision
// other than accept.
func (a Advisor) complete() error {
	for _, f := range []struct{ name, value string }{
		{"decision", a.Decision}, {"resolution", a.Resolution}, {"identity", a.Identity}, {"signed_at", a.SignedAt}} {
		if v := strings.TrimSpace(f.value); v == "" || strings.HasPrefix(strings.ToUpper(v), "TODO") {
			return fmt.Errorf("advisor_signoff.%s is blank or a placeholder", f.name)
		}
	}
	if a.Decision != evalcorpus.DecisionAccept {
		return fmt.Errorf("advisor_signoff.decision is %q; only %q can back an accepted manifest (a rejected or revised case needs a revised manifest and a new panel run)", a.Decision, evalcorpus.DecisionAccept)
	}
	return nil
}

// required checks presence separately from values, so false and zero cannot impersonate omissions.
func required(raw []byte, typ reflect.Type, path string) error {
	if typ.Kind() == reflect.Struct {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			tag := f.Tag.Get("json")
			name, opts, _ := strings.Cut(tag, ",")
			if name == "" || name == "-" {
				continue
			}
			v, ok := fields[name]
			if !ok || bytes.Equal(v, []byte("null")) && f.Type.Kind() != reflect.Slice && f.Type.Kind() != reflect.Map {
				if opts == "omitempty" {
					continue
				}
				return fmt.Errorf("%s.%s is required", path, name)
			}
			if err := required(v, f.Type, path+"."+name); err != nil {
				return err
			}
		}
	} else if typ.Kind() == reflect.Map && typ.Elem().Kind() == reflect.Struct {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for k, v := range fields {
			if err := required(v, typ.Elem(), path+"."+k); err != nil {
				return err
			}
		}
	} else if typ.Kind() == reflect.Slice && typ.Elem().Kind() == reflect.Struct {
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for i, v := range values {
			if err := required(v, typ.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
