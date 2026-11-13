//go:build linux

package main

import (
	"testing"

	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/sessionrepo"
)

func TestBundleRemovedCountCountsEveryRemovedObjectType(t *testing.T) {
	exists := func(typ delta.ObjectType) delta.ObjectState {
		return delta.ObjectState{Exists: true, Type: typ, MetadataIdentity: "m"}
	}
	var bundle sessionrepo.EvidenceBundle
	bundle.ValidatedChain.ComposedChanges = []delta.ObjectChange{
		{Path: "a", Before: exists(delta.ObjectRegular)},
		{Path: "d", Before: exists(delta.ObjectDirectory)},
		{Path: "created", After: exists(delta.ObjectRegular)},
		{Path: "modified", Before: exists(delta.ObjectRegular), After: exists(delta.ObjectRegular)},
	}
	if got := bundleRemovedCount(bundle); got != 2 {
		t.Fatalf("removed %d, want 2", got)
	}
	if got := bundleRemovedCount(sessionrepo.EvidenceBundle{}); got != 0 {
		t.Fatalf("an empty bundle removed %d", got)
	}
}

func TestParseProjectReadsTheNoDeleteAndRemovalLimitKeys(t *testing.T) {
	p, err := parseProject("id=a,writable=src,no-delete=src,auto-publish-max-removals=3")
	if err != nil || p.Scope == nil || len(p.Scope.NoDelete) != 1 || p.Scope.AutoPublishMaxRemovals == nil || *p.Scope.AutoPublishMaxRemovals != 3 {
		t.Fatalf("scope %+v, %v", p.Scope, err)
	}
	if _, err := parseProject("id=a,auto-publish-max-removals=x"); err == nil {
		t.Fatal("a non-numeric limit was accepted")
	}
}
