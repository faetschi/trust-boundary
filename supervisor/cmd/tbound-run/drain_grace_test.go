package main

import (
	"strings"
	"testing"
)

// proc.drain_grace_seconds is a frozen protocol parameter (D49): bounded 1..60, required, and part of the run binding.
func TestDrainGraceSecondsIsBoundedAndRequired(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(proc map[string]any)
		ok   bool
	}{
		{"min", func(p map[string]any) { p["drain_grace_seconds"] = 1 }, true},
		{"max", func(p map[string]any) { p["drain_grace_seconds"] = 60 }, true},
		{"zero", func(p map[string]any) { p["drain_grace_seconds"] = 0 }, false},
		{"negative", func(p map[string]any) { p["drain_grace_seconds"] = -1 }, false},
		{"too long", func(p map[string]any) { p["drain_grace_seconds"] = 61 }, false},
		{"missing", func(p map[string]any) { delete(p, "drain_grace_seconds") }, false},
	} {
		e := newEnv(t)
		tc.set(e.cfg["proc"].(map[string]any))
		e.write()
		_, err := loadConfig(e.cfgPath)
		if tc.ok && err != nil {
			t.Errorf("%s: refused: %v", tc.name, err)
		}
		if !tc.ok && (err == nil || !strings.Contains(err.Error(), "drain_grace_seconds")) {
			t.Errorf("%s: not refused by name: %v", tc.name, err)
		}
	}
}
