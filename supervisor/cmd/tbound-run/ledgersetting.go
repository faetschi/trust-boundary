package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
)

var headRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ledgerHead names a ledger state: the record count and the head hash after that
// many records (genesis is record 1, as in usdledger.Snapshot).
type ledgerHead struct {
	Records    uint64 `json:"records"`
	HeadSHA256 string `json:"head_sha256"`
}

// ledgerFile is the object form of run.json's "ledger". Checkpoint is the
// operator-held anchor the file must still contain, Final the exact state the
// report binds. Both are required.
type ledgerFile struct {
	Path                        string      `json:"path"`
	LedgerID                    string      `json:"ledger_id"`
	CapMicroUSD                 int64       `json:"cap_micro_usd"`
	MinPerUnitWorstCaseMicroUSD int64       `json:"min_per_unit_worst_case_micro_usd"`
	Checkpoint                  *ledgerHead `json:"checkpoint"`
	Final                       *ledgerHead `json:"final"`
}

// ledgerSetting is run.json's "ledger": the string "synthetic-empty" (file stays
// nil) or a ledgerFile object. set tells an absent member from the synthetic one.
type ledgerSetting struct {
	set  bool
	file *ledgerFile
}

func (l *ledgerSetting) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		if name != ledgerSyntheticEmpty {
			return fmt.Errorf("ledger %q: use %q or an object with a ledger file", name, ledgerSyntheticEmpty)
		}
		*l = ledgerSetting{set: true}
		return nil
	}
	var file ledgerFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	*l = ledgerSetting{set: true, file: &file}
	return nil
}

func (l ledgerSetting) validate() error {
	if !l.set {
		return errors.New("ledger is required")
	}
	f := l.file
	if f == nil {
		return nil
	}
	if !filepath.IsAbs(f.Path) {
		return fmt.Errorf("ledger.path %q must be an absolute path", f.Path)
	}
	if f.LedgerID == "" || f.CapMicroUSD <= 0 || f.MinPerUnitWorstCaseMicroUSD <= 0 {
		return errors.New("ledger needs ledger_id, a positive cap_micro_usd and a positive min_per_unit_worst_case_micro_usd")
	}
	for name, h := range map[string]*ledgerHead{"ledger.checkpoint": f.Checkpoint, "ledger.final": f.Final} {
		if h == nil || h.Records == 0 || !headRE.MatchString(h.HeadSHA256) {
			return fmt.Errorf("%s needs records (at least 1) and head_sha256 as 64 lowercase hex digits", name)
		}
	}
	if f.Final.Records < f.Checkpoint.Records {
		return errors.New("ledger.final.records is below ledger.checkpoint.records")
	}
	return nil
}
