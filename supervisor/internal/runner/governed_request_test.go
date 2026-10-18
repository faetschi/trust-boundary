package runner

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestGovernedRequestIsDeterministicAndCarriesCaseIDAndAttempt(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	step := s.Steps[0]
	first := GovernedRequestFor(s.Digest, step)
	second := GovernedRequestFor(s.Digest, step)
	if first != second {
		t.Fatalf("request is not deterministic: %+v %+v", first, second)
	}
	if first.TaskID != step.CaseID || first.Attempt != 1 {
		t.Fatalf("request must name the case and attempt 1: %+v", first)
	}
	if !strings.HasPrefix(first.RequestID, "tbr-") || len(first.RequestID) != len("tbr-")+32 {
		t.Fatalf("request id shape changed: %q", first.RequestID)
	}
	// A different plan, step or attempt gives a different request id.
	rerun := step
	rerun.Attempt = 2
	other := GovernedRequestFor(s.Digest, s.Steps[1])
	changedPlan := GovernedRequestFor("sha256:"+strings.Repeat("0", 64), step)
	second2 := GovernedRequestFor(s.Digest, rerun)
	if first.RequestID == other.RequestID || first.RequestID == changedPlan.RequestID || first.RequestID == second2.RequestID {
		t.Fatal("request ids collide across steps, plans or attempts")
	}
	if second2.Attempt != 2 || second2.TaskID != step.CaseID {
		t.Fatalf("the outage rerun must be attempt 2 of the same case: %+v", second2)
	}
}

func TestGovernedRequestArgvInCorpusModeNeverCarriesATask(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	step := s.Steps[0]
	request := GovernedRequestFor(s.Digest, step)
	argv, err := request.Argv()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"serve", "--pi", "--task-id", step.CaseID, "--role", "trajectory", "--attempt", "1",
		"--task-request-id", request.RequestID, "--bash-no-outputs"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
	for _, arg := range argv {
		if arg == "--task" || arg == "--prompt" {
			t.Fatalf("a corpus-mode client must not send task text: %q", argv)
		}
	}
}

func TestGovernedRequestRefusesAttemptsOutsideOneAndTwo(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	for _, attempt := range []int{0, 3, -1} {
		step := s.Steps[0]
		step.Attempt = attempt
		if _, err := GovernedRequestFor(s.Digest, step).Argv(); err == nil {
			t.Errorf("attempt %d produced a client invocation", attempt)
		}
	}
	for _, request := range []GovernedRequest{{Attempt: 1, RequestID: "r"}, {Attempt: 1, TaskID: "c"}} {
		if _, err := request.Argv(); err == nil {
			t.Errorf("incomplete request %+v produced a client invocation", request)
		}
	}
}

func TestGovernedCWithoutATransportStaysUnwired(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	if _, err := (GovernedC{PlanDigest: s.Digest}).Execute(context.Background(), s.Steps[0]); !errors.Is(err, ErrNotWired) {
		t.Fatalf("GovernedC.Execute = %v, want ErrNotWired", err)
	}
	request, err := (GovernedC{PlanDigest: s.Digest}).Request(s.Steps[0])
	if err != nil || request != GovernedRequestFor(s.Digest, s.Steps[0]) {
		t.Fatalf("GovernedC.Request = %+v, %v", request, err)
	}
	if _, err := (GovernedC{PlanDigest: s.Digest}).Request(findStep(t, s, firstCase(s, "adversarial"), RoleStaticReplay, CfgP)); err == nil {
		t.Fatal("a non-C step produced a governed request")
	}
}

// Every planned C and P step maps to exactly one governed role, and no two of them share a
// (case, role, attempt) admission: the roles of the P half differ from those of the C half.
func TestEveryPlannedCStepHasADistinctGovernedAdmission(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	seen := map[string]string{}
	roles := map[string]int{}
	for _, st := range s.Steps {
		if st.Configuration != CfgC && st.Configuration != CfgP {
			continue
		}
		request, err := (GovernedC{PlanDigest: s.Digest, Configuration: st.Configuration}).Request(st)
		if err != nil {
			t.Fatalf("C step %s has no governed request: %v", st.ID(), err)
		}
		id := request.TaskID + "/" + request.Role + "/" + strconv.Itoa(request.Attempt)
		if prior, dup := seen[id]; dup {
			t.Fatalf("steps %s and %s share the admission %s", prior, st.ID(), id)
		}
		seen[id] = st.ID()
		roles[request.Role]++
	}
	for _, role := range GovernedRoles() {
		if roles[role] == 0 {
			t.Errorf("no planned C or P step uses the governed role %q", role)
		}
		delete(roles, role)
	}
	if len(roles) != 0 {
		t.Errorf("planned steps use roles outside the closed set: %v", roles)
	}
}

func TestGovernedRoleSetIsClosedAndNamedAfterTheRunnerRoles(t *testing.T) {
	want := []string{"trajectory", "static-replay", "adaptive-replay", "variance-1", "variance-2", "compliant", "ambiguous", "p-static-replay", "p-adaptive-replay"}
	if !reflect.DeepEqual(GovernedRoles(), want) {
		t.Fatalf("governed roles = %q, want %q", GovernedRoles(), want)
	}
	for _, role := range []Role{RoleTrajectory, RoleStaticReplay, RoleAdaptiveReplay, RoleCompliant, RoleAmbiguous} {
		if got := governedRoleOf(Step{Configuration: CfgC, Role: role}); got != string(role) {
			t.Errorf("runner role %q maps to %q", role, got)
		}
	}
	for repeat, want := range map[int]string{1: "variance-1", 2: "variance-2", 0: "", 3: ""} {
		if got := governedRoleOf(Step{Configuration: CfgC, Role: RoleVarianceRepeat, Repeat: repeat}); got != want {
			t.Errorf("variance repeat %d maps to %q, want %q", repeat, got, want)
		}
	}
	if _, err := (GovernedRequest{TaskID: "c", RequestID: "r", Attempt: 1, Role: "variance-3"}).Argv(); err == nil {
		t.Error("a role outside the closed set produced a client invocation")
	}
}
