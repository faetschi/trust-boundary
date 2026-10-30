package drainproof

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/workflowrecovery"
)

// baseObservation is accepted by Prove: the owner is not live and its cleanup
// scope is absent with a receipt that matches the expected scope identity.
func baseObservation() OwnerObservation {
	return OwnerObservation{
		Owner: workflowrecovery.OwnerEpochIdentity{
			Epoch: 3,
			Incarnation: workflowrecovery.OwnerIncarnation{
				Digest:               "incarnation-a",
				BootID:               "boot-a",
				PID:                  4242,
				StartTimeTicks:       1000,
				PIDNamespaceInode:    7,
				EffectiveUID:         1000,
				CgroupIdentityDigest: "cgroup-a",
			},
		},
		BootID:             "boot-a",
		ObservedBootID:     "boot-a",
		StartTicks:         1000,
		ObservedStartTicks: 1000,
		ProcessLive:        false,
		Scope:              ScopeObservation{Absent: true, IdentityDigest: "scope-a"},

		CleanupReceiptIdentity: "receipt-a",
		ExpectedScopeIdentity:  "receipt-a",
		PendingEffects:         0,
	}
}

func TestProveAcceptsDrainedOwner(t *testing.T) {
	proof, err := Prove(baseObservation())
	if err != nil {
		t.Fatalf("Prove(base) error = %v, want nil", err)
	}
	if proof.Digest == "" || proof.Basis == "" {
		t.Fatalf("Prove(base) = %+v, want non-empty digest and basis", proof)
	}
}

func TestProveAcceptsOwnerWhoseRecordedProcessIsNotLive(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*OwnerObservation)
	}{
		{"PID reuse: start ticks differ", func(o *OwnerObservation) {
			o.ProcessLive = true
			o.ObservedStartTicks = 2000
		}},
		{"boot id differs", func(o *OwnerObservation) {
			o.ProcessLive = true
			o.ObservedBootID = "boot-b"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := baseObservation()
			tc.mutate(&obs)
			proof, err := Prove(obs)
			if err != nil {
				t.Fatalf("Prove() error = %v, want nil", err)
			}
			if proof.Digest == "" {
				t.Fatalf("Prove() digest empty")
			}
		})
	}
}

func TestProveRefusesWithErrNotDrained(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*OwnerObservation)
	}{
		{"live, same boot, same ticks", func(o *OwnerObservation) {
			o.ProcessLive = true
		}},
		{"live with empty observed boot id", func(o *OwnerObservation) {
			o.ProcessLive = true
			o.ObservedBootID = ""
		}},
		{"absent scope without receipt", func(o *OwnerObservation) {
			o.CleanupReceiptIdentity = ""
		}},
		{"absent scope receipt mismatch", func(o *OwnerObservation) {
			o.CleanupReceiptIdentity = "receipt-b"
		}},
		{"absent scope but populated", func(o *OwnerObservation) {
			o.Scope.Populated = true
		}},
		{"absent scope but processes counted", func(o *OwnerObservation) {
			o.Scope.ProcCount = 1
		}},
		{"absent scope but remove failed", func(o *OwnerObservation) {
			o.Scope.RemoveErr = errors.New("rm failed")
		}},
		{"populated scope", func(o *OwnerObservation) {
			o.Scope.Absent = false
			o.Scope.Populated = true
		}},
		{"unpopulated scope with processes", func(o *OwnerObservation) {
			o.Scope.Absent = false
			o.Scope.ProcCount = 2
		}},
		{"unpopulated scope with negative process count", func(o *OwnerObservation) {
			o.Scope.Absent = false
			o.Scope.ProcCount = -1
		}},
		{"unpopulated scope remove failed", func(o *OwnerObservation) {
			o.Scope.Absent = false
			o.Scope.RemoveErr = errors.New("rm failed")
		}},
		{"present scope never observed (zero value)", func(o *OwnerObservation) {
			o.Scope = ScopeObservation{}
		}},
		{"present scope with empty identity digest", func(o *OwnerObservation) {
			o.Scope.Absent = false
			o.Scope.IdentityDigest = ""
		}},
		{"present scope identity mismatches expected", func(o *OwnerObservation) {
			o.Scope.Absent = false
			o.Scope.IdentityDigest = "scope-b"
		}},
		{"present scope is foreign (not owner cgroup) with no expectation", func(o *OwnerObservation) {
			o.Scope = ScopeObservation{Absent: false, IdentityDigest: "scope-a"}
			o.ExpectedScopeIdentity = ""
		}},
		{"present scope matches expectation but not owner cgroup", func(o *OwnerObservation) {
			o.Scope = ScopeObservation{Absent: false, IdentityDigest: "receipt-a"}
		}},
		{"owner pid zero", func(o *OwnerObservation) {
			o.Owner.Incarnation.PID = 0
		}},
		{"owner pid negative", func(o *OwnerObservation) {
			o.Owner.Incarnation.PID = -1
		}},
		{"owner start ticks zero on recorded and owner", func(o *OwnerObservation) {
			o.Owner.Incarnation.StartTimeTicks = 0
			o.StartTicks = 0
			o.ObservedStartTicks = 0
		}},
		{"owner cgroup identity digest empty", func(o *OwnerObservation) {
			o.Owner.Incarnation.CgroupIdentityDigest = ""
		}},
		{"pending effects", func(o *OwnerObservation) {
			o.PendingEffects = 1
		}},
		{"negative pending effects", func(o *OwnerObservation) {
			o.PendingEffects = -1
		}},
		{"recorded boot id disagrees with owner incarnation", func(o *OwnerObservation) {
			o.BootID = "boot-b"
			o.ObservedBootID = "boot-b"
		}},
		{"recorded start ticks disagree with owner incarnation", func(o *OwnerObservation) {
			o.StartTicks = 999
			o.ObservedStartTicks = 999
		}},
		{"recorded boot id empty", func(o *OwnerObservation) {
			o.BootID = ""
			o.ObservedBootID = ""
		}},
		{"owner epoch zero", func(o *OwnerObservation) {
			o.Owner.Epoch = 0
		}},
		{"owner incarnation digest empty", func(o *OwnerObservation) {
			o.Owner.Incarnation.Digest = ""
		}},
		{"owner incarnation boot id empty", func(o *OwnerObservation) {
			o.Owner.Incarnation.BootID = ""
		}},
		{"zero observation", func(o *OwnerObservation) {
			*o = OwnerObservation{}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := baseObservation()
			tc.mutate(&obs)
			checkRefusal(t, obs, ErrNotDrained)
		})
	}
}

func TestProvePopulatedDeadlineIsUnknownNotNotDrained(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*OwnerObservation)
	}{
		{"populated scope", func(o *OwnerObservation) {
			o.Scope.Absent = false
			o.Scope.Populated = true
		}},
		{"unpopulated scope with processes", func(o *OwnerObservation) {
			o.Scope.Absent = false
			o.Scope.ProcCount = 1
		}},
		{"absent scope", func(o *OwnerObservation) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := baseObservation()
			tc.mutate(&obs)
			obs.PopulatedDeadlineExceeded = true
			checkRefusal(t, obs, ErrUnknown)
		})
	}
}

// checkRefusal asserts Prove refuses obs with an error that wraps want, returns
// the zero Proof, and does not wrap the other sentinel.
func checkRefusal(t *testing.T, obs OwnerObservation, want error) {
	t.Helper()
	proof, err := Prove(obs)
	if err == nil {
		t.Fatalf("Prove() = %+v, nil; want error wrapping %v", proof, want)
	}
	if !errors.Is(err, want) {
		t.Fatalf("Prove() error = %v; want errors.Is %v", err, want)
	}
	if want == ErrUnknown && errors.Is(err, ErrNotDrained) {
		t.Fatalf("Prove() error = %v; unknown outcome must not wrap ErrNotDrained", err)
	}
	if proof != (Proof{}) {
		t.Fatalf("Prove() refusal returned non-zero proof %+v", proof)
	}
}

func TestProveAllFailsClosedOnEmptyOrAnyRefusal(t *testing.T) {
	cases := []struct {
		name string
		obs  []OwnerObservation
	}{
		{"nil slice", nil},
		{"empty slice", []OwnerObservation{}},
		{"one live owner poisons the batch", []OwnerObservation{baseObservation(), liveObservation()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proofs, err := ProveAll(tc.obs)
			if err == nil {
				t.Fatalf("ProveAll() = %+v, nil; want error", proofs)
			}
			if !errors.Is(err, ErrNotDrained) {
				t.Fatalf("ProveAll() error = %v; want errors.Is ErrNotDrained", err)
			}
			if proofs != nil {
				t.Fatalf("ProveAll() refusal returned proofs %+v", proofs)
			}
		})
	}
}

func TestProveAllReturnsPerOwnerProofs(t *testing.T) {
	second := baseObservation()
	second.Owner.Epoch = 4
	second.Owner.Incarnation.Digest = "incarnation-b"
	obs := []OwnerObservation{baseObservation(), second}

	proofs, err := ProveAll(obs)
	if err != nil {
		t.Fatalf("ProveAll() error = %v, want nil", err)
	}
	if len(proofs) != len(obs) {
		t.Fatalf("ProveAll() returned %d proofs, want %d", len(proofs), len(obs))
	}
	for i, o := range obs {
		single, err := Prove(o)
		if err != nil {
			t.Fatalf("Prove(obs[%d]) error = %v", i, err)
		}
		if proofs[i] != single {
			t.Fatalf("ProveAll()[%d] = %+v, want %+v", i, proofs[i], single)
		}
	}
	if proofs[0].Digest == proofs[1].Digest {
		t.Fatalf("distinct owners produced equal digests %q", proofs[0].Digest)
	}
}

// liveObservation is refused: its recorded process is still live on the same
// boot with the same start ticks.
func liveObservation() OwnerObservation {
	obs := baseObservation()
	obs.ProcessLive = true
	return obs
}

func TestObservationDigestIsDeterministic(t *testing.T) {
	first := observationDigest(baseObservation())
	second := observationDigest(baseObservation())
	if first == "" {
		t.Fatalf("observationDigest() is empty")
	}
	if first != second {
		t.Fatalf("observationDigest() not deterministic: %q != %q", first, second)
	}
	proof, err := Prove(baseObservation())
	if err != nil {
		t.Fatalf("Prove(base) error = %v", err)
	}
	if proof.Digest != first {
		t.Fatalf("Prove(base).Digest = %q, want observationDigest %q", proof.Digest, first)
	}
}

// TestObservationDigestSensitivityPerObservedField mutates every scalar leaf of
// the observation, one at a time, and requires the digest to change. The test
// fails if a field is added or removed without updating the expected count.
func TestObservationDigestSensitivityPerObservedField(t *testing.T) {
	base := baseObservation()
	baseDigest := observationDigest(base)

	const wantLeaves = 25
	leaves := 0
	for n := 0; ; n++ {
		mutated, path, found := mutateLeafN(base, n)
		if !found {
			break
		}
		leaves++
		changed := observationDigest(mutated) != baseDigest
		if !changed {
			t.Errorf("changing %s did not change the digest", path)
		}
	}
	if leaves != wantLeaves {
		t.Fatalf("visited %d leaves, want %d: update this test when observation fields change", leaves, wantLeaves)
	}
}

// walkLeaves calls visit for each non-struct value reachable from v, recursing
// into struct fields in declaration order.
func walkLeaves(v reflect.Value, path string, visit func(string, reflect.Value)) {
	if v.Kind() != reflect.Struct {
		visit(path, v)
		return
	}
	for i := 0; i < v.NumField(); i++ {
		walkLeaves(v.Field(i), path+"."+v.Type().Field(i).Name, visit)
	}
}

// mutateLeafN returns a copy of base with its n-th leaf changed, and that leaf's
// path. found is false when n is past the last leaf.
func mutateLeafN(base OwnerObservation, n int) (OwnerObservation, string, bool) {
	cp := reflect.New(reflect.TypeOf(base)).Elem()
	cp.Set(reflect.ValueOf(base))
	path, found, idx := "", false, 0
	walkLeaves(cp, "", func(p string, leaf reflect.Value) {
		if idx == n {
			mutateLeaf(leaf)
			path, found = p, true
		}
		idx++
	})
	return cp.Interface().(OwnerObservation), path, found
}

func mutateLeaf(leaf reflect.Value) {
	switch leaf.Kind() {
	case reflect.String:
		leaf.SetString(leaf.String() + "~")
	case reflect.Uint32, reflect.Uint64:
		leaf.SetUint(leaf.Uint() + 1)
	case reflect.Int:
		leaf.SetInt(leaf.Int() + 1)
	case reflect.Bool:
		leaf.SetBool(!leaf.Bool())
	case reflect.Interface:
		leaf.Set(reflect.ValueOf(errors.New("mutated observation error")))
	default:
		panic("drainproof test: unhandled leaf kind " + leaf.Kind().String())
	}
}

func TestObservationDigestIsNotConcatenationAmbiguous(t *testing.T) {
	a := baseObservation()
	a.Owner.Incarnation.Digest = "ab"
	a.Owner.Incarnation.BootID = "c"
	b := baseObservation()
	b.Owner.Incarnation.Digest = "a"
	b.Owner.Incarnation.BootID = "bc"
	if observationDigest(a) == observationDigest(b) {
		t.Fatalf("adjacent strings with shifted boundary produced equal digests")
	}
}

func TestZeroValueObservationDoesNotPanic(t *testing.T) {
	var zero OwnerObservation
	_ = observationDigest(zero)
	checkRefusal(t, zero, ErrNotDrained)
	if _, err := ProveAll([]OwnerObservation{zero}); !errors.Is(err, ErrNotDrained) {
		t.Fatalf("ProveAll(zero) error = %v; want ErrNotDrained", err)
	}
	if _, err := ProveAll(nil); !errors.Is(err, ErrNotDrained) {
		t.Fatalf("ProveAll(nil) error = %v; want ErrNotDrained", err)
	}
}

// A present scope is accepted only when its observed identity equals the owner's
// cgroup identity and, if an expected identity is set, equals it too.
func TestProvePresentScopeAcceptsOwnerCgroupIdentity(t *testing.T) {
	cases := []struct {
		name     string
		expected string
	}{
		{"observed identity equals expected", "cgroup-a"},
		{"no expected identity set, observed identity is the owner cgroup", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := baseObservation()
			obs.Scope = ScopeObservation{Absent: false, IdentityDigest: "cgroup-a"}
			obs.ExpectedScopeIdentity = tc.expected
			proof, err := Prove(obs)
			if err != nil {
				t.Fatalf("Prove() error = %v, want nil", err)
			}
			if proof.Digest == "" {
				t.Fatalf("Prove() digest empty")
			}
		})
	}
}

// Two accepted proofs that differ only in ExpectedScopeIdentity must not share a
// digest.
func TestDigestCoversExpectedScopeIdentity(t *testing.T) {
	a := baseObservation()
	a.Scope = ScopeObservation{Absent: false, IdentityDigest: "cgroup-a"}
	a.ExpectedScopeIdentity = ""
	b := a
	b.ExpectedScopeIdentity = "cgroup-a"

	pa, errA := Prove(a)
	pb, errB := Prove(b)
	if errA != nil || errB != nil {
		t.Fatalf("Prove errors = %v, %v; want nil", errA, errB)
	}
	if pa.Digest == pb.Digest {
		t.Fatalf("expected-identity change did not change the proof digest %q", pa.Digest)
	}
}

// The digest covers the observed scope identity on the present path. It is
// checked on the observation directly, because a changed identity is refused.
func TestPresentScopeDigestCoversObservedIdentity(t *testing.T) {
	base := baseObservation()
	base.Scope = ScopeObservation{Absent: false, IdentityDigest: "cgroup-a"}
	changed := base
	changed.Scope.IdentityDigest = "cgroup-b"
	if observationDigest(base) == observationDigest(changed) {
		t.Fatalf("present-scope identity change did not change observationDigest")
	}
}

// A zero observation with a valid owner must be refused by the scope step itself,
// not by owner validation.
func TestZeroValueObservationWithValidOwnerRefusedAtScope(t *testing.T) {
	owner := baseObservation()
	cases := map[string]OwnerObservation{
		"zero scope": func() OwnerObservation {
			o := baseObservation()
			o.Scope = ScopeObservation{}
			return o
		}(),
		"zero everything but owner and recorded identity": {
			Owner:      owner.Owner,
			BootID:     owner.BootID,
			StartTicks: owner.StartTicks,
		},
	}
	for name, obs := range cases {
		t.Run(name, func(t *testing.T) {
			checkRefusal(t, obs, ErrNotDrained)
			_, err := Prove(obs)
			if err == nil || !strings.Contains(err.Error(), "no observed identity") {
				t.Fatalf("Prove() error = %v; want refusal from the scope step", err)
			}
		})
	}
}

// neverBoundObservation is a dead owner whose cleanup receipt says no worker
// scope was ever bound. There is no scope identity, receipt identity or expectation.
func neverBoundObservation() OwnerObservation {
	obs := baseObservation()
	obs.Scope = ScopeObservation{Absent: true, NeverBound: true, WorkflowID: "workflow-a", ScopeName: "pi-wf-a-e3"}
	obs.CleanupReceiptIdentity = ""
	obs.ExpectedScopeIdentity = ""
	return obs
}

func TestProveAcceptsNeverBoundOwner(t *testing.T) {
	proof, err := Prove(neverBoundObservation())
	if err != nil {
		t.Fatalf("Prove(never bound) error = %v, want nil", err)
	}
	if !strings.Contains(proof.Basis, "scope-never-bound-receipt") || strings.Contains(proof.Basis, "receipt-matched") {
		t.Fatalf("Basis = %q, want the never-bound basis and not the bound-scope basis", proof.Basis)
	}
}

func TestProveRefusesInconsistentNeverBoundOwner(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*OwnerObservation)
	}{
		{"live owner", func(o *OwnerObservation) { o.ProcessLive = true }},
		{"scope not absent", func(o *OwnerObservation) { o.Scope.Absent = false }},
		{"scope populated", func(o *OwnerObservation) { o.Scope.Populated = true }},
		{"scope has processes", func(o *OwnerObservation) { o.Scope.ProcCount = 1 }},
		{"scope remove error", func(o *OwnerObservation) { o.Scope.RemoveErr = errors.New("rmdir failed") }},
		{"also carries a scope identity", func(o *OwnerObservation) { o.Scope.IdentityDigest = "scope-a" }},
		{"also carries a receipt identity", func(o *OwnerObservation) { o.CleanupReceiptIdentity = "receipt-a" }},
		{"also carries an expected identity", func(o *OwnerObservation) { o.ExpectedScopeIdentity = "receipt-a" }},
		{"pending effects", func(o *OwnerObservation) { o.PendingEffects = 1 }},
		{"no workflow id", func(o *OwnerObservation) { o.Scope.WorkflowID = "" }},
		{"no scope name", func(o *OwnerObservation) { o.Scope.ScopeName = "" }},
		{"owner epoch zero", func(o *OwnerObservation) { o.Owner.Epoch = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := neverBoundObservation()
			tc.mutate(&obs)
			checkRefusal(t, obs, ErrNotDrained)
		})
	}
}

// Absent with no receipt and no NeverBound mark stays refused: an identity-free
// absent scope is not a receipt.
func TestProveStillRefusesAbsentScopeWithoutReceiptOrNeverBound(t *testing.T) {
	obs := neverBoundObservation()
	obs.Scope.NeverBound = false
	checkRefusal(t, obs, ErrNotDrained)
}

// The never-bound proof must not share a digest with any bound-scope receipt
// proof, including a bound receipt that has empty identity text.
func TestNeverBoundDigestIsDistinctFromBoundReceiptDigest(t *testing.T) {
	never := neverBoundObservation()
	neverProof, err := Prove(never)
	if err != nil {
		t.Fatal(err)
	}
	bound := baseObservation()
	boundProof, err := Prove(bound)
	if err != nil {
		t.Fatal(err)
	}
	if neverProof.Digest == boundProof.Digest {
		t.Fatal("never-bound and bound-scope proofs share a digest")
	}
	// Same facts except the mark itself.
	unmarked := never
	unmarked.Scope.NeverBound = false
	if observationDigest(never) == observationDigest(unmarked) {
		t.Fatal("the NeverBound mark does not change the digest")
	}
}
