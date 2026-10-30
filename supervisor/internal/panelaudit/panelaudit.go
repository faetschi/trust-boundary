// Package panelaudit verifies optional supplementary panel diagnostics and their evidence bindings.
package panelaudit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/evalcorpus"
)

const (
	ReceiptSchema = "tbound-panel-receipt/v3"
	AuditSchema   = "tbound-panel-audit/v4"
	// MaxFileBytes bounds each file; the importer reads at most this much.
	MaxFileBytes = 16 << 20
)

// Advisor retains historical annotations. They are optional and do not authorize admission.
type Advisor struct {
	Decision   string `json:"decision"`
	Resolution string `json:"resolution"`
	Identity   string `json:"identity"`
	SignedAt   string `json:"signed_at"`
}

// Receipt is the part of receipt.json that Verify reads.
type Receipt struct {
	Provider string  `json:"provider"`
	LiveRun  bool    `json:"live_run"`
	Models   []Model `json:"models"`
	// PublicPolicy is the policy context both phases showed the raters.
	PublicPolicy  PublicPolicy `json:"public_policy"`
	CallsRecorded int          `json:"calls_recorded"`
	Outcomes      []Outcome    `json:"rating_outcomes"`
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

// Audit is the part of audit.json that Verify reads. ReceiptDigest is the Digest of the exact receipt.json
// the audit was written with.
type Audit struct {
	Provider      string      `json:"provider"`
	LiveRun       bool        `json:"live_run"`
	ReceiptDigest string      `json:"receipt_digest"`
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

// Verify checks supplied evidence, optional manifest pins, content and receipt bindings,
// rating coverage, recomputed diagnostics and seeded selection. It makes no admission or human-review claim.
func Verify(m evalcorpus.Manifest, receiptRaw, auditRaw, statsRaw []byte) error {
	cr := m.ConstructReview
	if cr != (evalcorpus.ConstructReview{}) {
		if got := Digest(receiptRaw); got != cr.PanelReceiptDigest {
			return fmt.Errorf("panel receipt digest %s is not construct_review.panel_receipt_digest %s", got, cr.PanelReceiptDigest)
		}
		if got := Digest(auditRaw); got != cr.PanelAuditDigest {
			return fmt.Errorf("panel audit digest %s is not construct_review.panel_audit_digest %s", got, cr.PanelAuditDigest)
		}
		if got := Digest(statsRaw); got != cr.PanelStatsDigest {
			return fmt.Errorf("panel stats digest %s is not construct_review.panel_stats_digest %s", got, cr.PanelStatsDigest)
		}
	}
	content, err := evalcorpus.ContentDigest(m)
	if err != nil {
		return err
	}
	if cr != (evalcorpus.ConstructReview{}) && cr.ContentDigest != content {
		return errors.New("construct_review.content_digest differs from manifest content")
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
	if stats.ContentDigest != content {
		return errors.New("panel stats content_digest differs from manifest")
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
	if audit.ReceiptDigest != Digest(receiptRaw) {
		return fmt.Errorf("panel audit receipt_digest %q is not the digest %s of the panel receipt", audit.ReceiptDigest, Digest(receiptRaw))
	}
	if receipt.DryRun || audit.DryRun || !receipt.LiveRun || !audit.LiveRun || receipt.Provider != "openrouter" || audit.Provider != "openrouter" {
		return errors.New("the panel run is a dry run: its ratings are canned fake data")
	}
	if receipt.ContentDigest != content {
		return fmt.Errorf("panel receipt content_digest %s is not the content digest %s of the manifest", receipt.ContentDigest, content)
	}
	if audit.ContentDigest != content {
		return fmt.Errorf("panel audit content_digest %s is not the content digest %s of the manifest", audit.ContentDigest, content)
	}
	var modelIDs []string
	for _, model := range receipt.Models {
		modelIDs = append(modelIDs, model.ID)
	}
	if !slices.Equal(stats.Models, modelIDs) {
		return errors.New("panel stats models differ from receipt models")
	}
	if err := verifyCoverage(m, receipt); err != nil {
		return err
	}
	incomplete, err := verifyCompletion(m, receipt, stats)
	if err != nil {
		return err
	}
	want := ChecklistStats(m.Cases, receipt.Outcomes, modelIDs, 0, 0)
	if incomplete {
		for key, item := range want {
			item.Gate = GateIncomplete
			want[key] = item
		}
	}
	if err := verifyStats(stats, want); err != nil {
		return err
	}
	if err := verifyAudited(m, audit); err != nil {
		return err
	}
	return verifySelection(m, audit, receipt)
}

// verifyStats refuses stats.json unless each checklist item matches the statistics recomputed
// from the pinned ratings; the diagnostic classification does not govern admission.
func verifyStats(stats Stats, want map[string]ItemStats) error {
	if len(stats.Items) != len(want) {
		return errors.New("panel stats checklist items differ from the registered checklist")
	}
	const tolerance = 1e-9
	near := func(a, b float64) bool { return math.Abs(a-b) <= tolerance }
	for _, item := range Checklist {
		got, w := stats.Items[item.Key], want[item.Key]
		var diffs []string
		differ := func(name string, got, want any, same bool) {
			if !same {
				diffs = append(diffs, fmt.Sprintf("%s is %v but the receipt ratings give %v", name, got, want))
			}
		}
		differ("subjects", got.Subjects, w.Subjects, got.Subjects == w.Subjects)
		differ("roots", got.Roots, w.Roots, got.Roots == w.Roots)
		differ("ac1", got.AC1, w.AC1, got.AC1Defined == w.AC1Defined && near(got.AC1, w.AC1))
		differ("kappa", got.Kappa, w.Kappa, got.KappaDefined == w.KappaDefined && near(got.Kappa, w.Kappa))
		differ("raw_agreement", got.Agreement, w.Agreement, near(got.Agreement, w.Agreement))
		differ("unanimous_share", got.Unanimous, w.Unanimous, near(got.Unanimous, w.Unanimous))
		samePrevalence := len(got.Prevalence) == len(w.Prevalence)
		for cat, p := range w.Prevalence {
			g, ok := got.Prevalence[cat]
			samePrevalence = samePrevalence && ok && near(g, p)
		}
		differ("prevalence", got.Prevalence, w.Prevalence, samePrevalence)
		if len(diffs) > 0 {
			return fmt.Errorf("panel stats item %s numeric field %s", item.Key, diffs[0])
		}
		if got.Gate != w.Gate {
			return fmt.Errorf("panel stats item %s gate %q contradicts the gate %q recomputed from the receipt ratings", item.Key, got.Gate, w.Gate)
		}
	}
	return nil
}

func verifyAudited(m evalcorpus.Manifest, audit Audit) error {
	byID := make(map[string]bool, len(m.Cases))
	for _, c := range m.Cases {
		byID[c.CaseID] = true
	}
	listed := make(map[string]bool, len(audit.Cases))
	for _, ac := range audit.Cases {
		if !byID[ac.CaseID] {
			return fmt.Errorf("panel audit lists case %q, which the manifest does not have", ac.CaseID)
		}
		if listed[ac.CaseID] {
			return fmt.Errorf("panel audit lists case %q twice", ac.CaseID)
		}
		listed[ac.CaseID] = true
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
			if !ok && f.Type == reflect.TypeOf(Advisor{}) {
				continue
			}
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
