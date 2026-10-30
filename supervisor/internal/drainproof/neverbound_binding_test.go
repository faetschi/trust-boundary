package drainproof

import "testing"

// Round-2 review: the NeverBound proof digest must bind the workflow and the
// scope name, so two workflows at one epoch under one owner incarnation do not
// share a digest. A bound-scope proof gets its uniqueness from the scope
// identity; a never-bound receipt has none, so it carries these two instead.

func TestNeverBoundDigestBindsWorkflowAndScopeName(t *testing.T) {
	a := neverBoundObservation()
	b := neverBoundObservation()
	b.Scope.WorkflowID = "workflow-b" // same owner, same epoch, another workflow
	b.Scope.ScopeName = "pi-wf-b-e3"
	pa, err := Prove(a)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := Prove(b)
	if err != nil {
		t.Fatal(err)
	}
	if pa.Digest == pb.Digest {
		t.Fatal("two workflows at one epoch under one owner share a never-bound digest")
	}
	onlyWorkflow := neverBoundObservation()
	onlyWorkflow.Scope.WorkflowID = "workflow-b"
	onlyName := neverBoundObservation()
	onlyName.Scope.ScopeName = "pi-wf-b-e3"
	if observationDigest(onlyWorkflow) == pa.Digest || observationDigest(onlyName) == pa.Digest {
		t.Fatal("the digest does not bind both the workflow id and the scope name")
	}
	if observationDigest(onlyWorkflow) == observationDigest(onlyName) {
		t.Fatal("workflow id and scope name are interchangeable in the digest")
	}
}

// The label is part of the digest contract: it names the encoding shape. v3 adds
// the scope's workflow id and scope name to the v2 shape. Pin the exact value and
// two golden digests, so changing the label, the field order or the encoding
// fails here and cannot pass unnoticed.
func TestObservationDigestLabelAndGoldenValues(t *testing.T) {
	const wantLabel = "tbound/supervisor/drainproof/observation/v3"
	if digestLabel != wantLabel {
		t.Fatalf("digestLabel = %q, want %q", digestLabel, wantLabel)
	}
	golden := []struct {
		name string
		obs  OwnerObservation
		want string
	}{
		{"bound receipt", baseObservation(), goldenBoundDigest},
		{"never bound", neverBoundObservation(), goldenNeverBoundDigest},
	}
	for _, g := range golden {
		if got := observationDigest(g.obs); got != g.want {
			t.Errorf("%s digest = %s, want %s", g.name, got, g.want)
		}
		proof, err := Prove(g.obs)
		if err != nil || proof.Digest != g.want {
			t.Errorf("%s: Prove digest = %q (%v), want %s", g.name, proof.Digest, err, g.want)
		}
	}
}

const (
	goldenBoundDigest      = "c116a6fe384621c8e160ec7dd756b7affd087df0144bc078dd1f2b75e5473020"
	goldenNeverBoundDigest = "d7aa6bdd64e3905de66a6de20e1d2c1e42d31b7ffed14264e903cee7ea1931c4"
)
