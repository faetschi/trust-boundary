//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductReviewLimitsNeverReadCellCgroup(t *testing.T) {
	if strings.Contains(productQualificationLimitsScript, "/sys/fs/cgroup") {
		t.Fatal("limits still depend on files excluded by frozen Landlock")
	}
}

func TestProductReviewDNSNegativeAnswerIsNotIsolation(t *testing.T) {
	for _, errno := range []string{"ENOTFOUND", "EAI_NONAME"} {
		var report productNetworkReport
		json.Unmarshal(productDeniedNetworkOutput(t, "127.0.0.1:41234", nil), &report)
		report.Checks[0].Errno = errno
		if verifyProductDenyAllNetwork(report) == nil {
			t.Fatalf("accepted negative DNS answer %s", errno)
		}
	}
}

func TestProductReviewUnroutedInterfaceIsNotIsolation(t *testing.T) {
	raw := productDeniedNetworkOutput(t, "127.0.0.1:41234", nil)
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	fields["interfaces"] = json.RawMessage(`["lo","eth0"]`)
	raw, _ = json.Marshal(fields)
	var report productNetworkReport
	json.Unmarshal(raw, &report)
	if verifyProductDenyAllNetwork(report) == nil {
		t.Fatal("accepted extra interface without routes")
	}
}

func TestProductReviewFailedSettlementKeepsQualificationHandles(t *testing.T) {
	h := newQualificationHarness(t)
	env := h.newEnv(privateGovernedTestRoot(t))
	h.install(t, h.steps())
	cell, err := openProductQualificationCell(context.Background(), h.target(), env, h.newRunner)
	if err != nil {
		t.Fatal(err)
	}
	r := cell.(*productQualificationRun)
	h.cleanupErr = errors.New("unresolved container")
	if r.finish(context.Background(), errors.New("phase failed")) == nil {
		t.Fatal("settlement passed")
	}
	if r.closed {
		t.Fatal("failed settlement released dependent handles")
	}
	h.cleanupErr = nil
	if err := r.runner.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.release(); err != nil {
		t.Fatal(err)
	}
}

func TestProductReviewQualificationRetention(t *testing.T) {
	h := newQualificationHarness(t)
	env := h.newEnv(privateGovernedTestRoot(t))
	h.install(t, h.steps())
	for i := 0; i < 7; i++ {
		cell, err := openProductQualificationCell(context.Background(), h.target(), env, h.newRunner)
		if err != nil {
			t.Fatal(err)
		}
		if err := cell.finish(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(env.StateRoot, productQualificationDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("retained %d fully drained runs, want 5", len(entries))
	}
}
