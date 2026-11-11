package productsession

import (
	"errors"
	"path/filepath"
	"testing"
)

func limitedGamma(limit *int) func(*Registration) {
	return func(r *Registration) {
		r.Projects[2].Scope = &ProductScope{Writable: []string{"src"}, AutoPublishMaxRemovals: limit}
	}
}

func intp(n int) *int { return &n }

func TestRegistrationRefusesAnOutOfRangeRemovalLimit(t *testing.T) {
	_, priv := fixtureKey()
	for name, limit := range map[string]*int{"negative": intp(-1), "above the ceiling": intp(1<<20 + 1)} {
		r := fixtureUnsigned()
		limitedGamma(limit)(&r)
		if _, _, err := SignRegistration(r, priv); !errors.Is(err, ErrRegistration) {
			t.Errorf("%s: err=%v, want ErrRegistration", name, err)
		}
	}
	for name, limit := range map[string]*int{"nil": nil, "zero": intp(0), "ceiling": intp(1 << 20)} {
		reg := signedRegistration(t, limitedGamma(limit))
		if got := reg.Projects[2].Scope; !sameScope(got, &ProductScope{Writable: []string{"src"}, AutoPublishMaxRemovals: limit}) {
			t.Errorf("%s: round trip changed the scope: %+v", name, got)
		}
	}
}

func TestRemovalLimitIsPartOfTheScopeIdentity(t *testing.T) {
	a := &ProductScope{Writable: []string{"src"}, AutoPublishMaxRemovals: intp(3)}
	for name, other := range map[string]*ProductScope{
		"no limit":        {Writable: []string{"src"}},
		"another limit":   {Writable: []string{"src"}, AutoPublishMaxRemovals: intp(4)},
		"zero vs nonzero": {Writable: []string{"src"}, AutoPublishMaxRemovals: intp(0)},
	} {
		if sameScope(a, other) {
			t.Errorf("%s: scopes compare equal", name)
		}
	}
	got := a.clone()
	*got.AutoPublishMaxRemovals = 9
	if *a.AutoPublishMaxRemovals != 3 {
		t.Error("clone shares the limit with the original")
	}
	if a.AdmissionScope().IsZero() || len(a.AdmissionScope().Writable) != 1 {
		t.Error("the limit must not change the admission scope")
	}
}

func TestAutoTurnAwaitsReviewOnlyWithARemovalLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit *int
		want  bool
	}{{"a limited auto turn", intp(1), true}, {"an auto turn without a limit", nil, false}} {
		c := openAt(t, filepath.Join(privateDir(t), "catalog.jsonl"), signedRegistration(t, limitedGamma(tc.limit)))
		s := mustSession(t, c, uid1, "gamma", "auto", bigBudget, "s")
		turn := mustSubmit(t, c, uid1, s.ID, 0, "k", "p")
		mustLink(t, c, turn.ID)
		err := c.MarkAwaitingReview(turn.ID, reviewFor(c, turn.ID, reviewUsage, candidate))
		if tc.want != (err == nil) {
			t.Fatalf("%s: err=%v", tc.name, err)
		}
		if !tc.want {
			wantCode(t, err, CodeInvalidRequest)
			continue
		}
		reviewed, _ := c.Turn(turn.ID)
		mustResolve(t, c, reviewed, "r", DecisionPublish)
		if err := c.CompleteTurn(turn.ID, terminalFor(c, turn.ID, OutcomePublished, "", candidate, reviewUsage)); err != nil {
			t.Fatalf("publish after the review: %v", err)
		}
		if got, _ := c.Turn(turn.ID); got.State != StateTerminal || got.Outcome != OutcomePublished {
			t.Fatalf("turn is %s/%s, want published", got.State, got.Outcome)
		}
	}
}
