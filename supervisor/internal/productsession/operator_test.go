//go:build linux

package productsession

import (
	"encoding/json"
	"os"
	"os/exec"
	"tbound/supervisor/internal/audit"
	"testing"
)

func operatorEvidence(t Turn) (OperatorEvidence, error) {
	return OperatorEvidence{WorkflowID: t.Link.WorkflowID, BaselineDigest: t.BaselineDigest, ScanDigest: t.BaselineDigest,
		StoreHead: dg("store"), ProviderHead: dg("provider"), CommandHead: sha256Digest(nil), CheckpointDigest: dg("checkpoint")}, nil
}

func TestOperatorResolution(t *testing.T) {
	for _, mode := range []string{"success", "review", "dry-run", "not-unknown", "reason", "changed", "locked"} {
		t.Run(mode, func(t *testing.T) {
			c, path := newCatalog(t)
			reg := c.st.reg
			publicationMode := "auto"
			if mode == "review" {
				publicationMode = "review"
			}
			s := mustSession(t, c, uid1, "alpha", publicationMode, bigBudget, "s")
			turn := mustSubmit(t, c, uid1, s.ID, 0, "k", "prompt")
			mustLink(t, c, turn.ID)
			if mode == "review" {
				// Review-mode usage is settled before a later unknown outcome.
				if err := c.MarkAwaitingReview(turn.ID, reviewFor(c, turn.ID, Budget{1, 1, 1}, dg("candidate"))); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "not-unknown" {
				if err := c.MarkTurnUnknown(turn.ID, "lost provider"); err != nil {
					t.Fatal(err)
				}
			}
			turn, _ = c.Turn(turn.ID)
			before, _ := os.ReadFile(path)
			if mode != "locked" {
				c.Close()
			}
			reason := "provider failed; no effects"
			if mode == "reason" {
				reason = " "
			}
			check := operatorEvidence
			if mode == "changed" {
				check = func(turn Turn) (OperatorEvidence, error) {
					e, _ := operatorEvidence(turn)
					e.ScanDigest = b1
					return e, nil
				}
			}
			got, err := OperatorResolve(path, reg, turn.ID, reason, mode == "dry-run", check)
			if mode != "success" && mode != "review" && mode != "dry-run" {
				if err == nil {
					t.Fatal("unsafe resolution accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "dry-run" {
				after, _ := os.ReadFile(path)
				if string(after) != string(before) {
					t.Fatal("dry-run wrote catalog")
				}
				return
			}
			if got.Outcome != "abandoned" || got.ContextEligible || got.Refund != (Budget{}) || got.Usage != turn.Reservation {
				t.Fatalf("resolved: %+v", got)
			}
			child := exec.Command(os.Args[0], "-test.run=^TestOperatorResolutionFreshProcess$")
			child.Env = append(os.Environ(), "TB_OPERATOR_CATALOG="+path, "TB_OPERATOR_SESSION="+s.ID, "TB_OPERATOR_TURN="+turn.ID)
			if out, err := child.CombinedOutput(); err != nil {
				t.Fatalf("fresh replay: %v %s", err, out)
			}
			if _, err := OperatorResolve(path, reg, turn.ID, reason, false, check); err == nil {
				t.Fatal("duplicate accepted")
			}
		})
	}
}

func TestOperatorResolutionFreshProcess(t *testing.T) {
	path := os.Getenv("TB_OPERATOR_CATALOG")
	if path == "" {
		return
	}
	c := openAt(t, path, signedRegistration(t, nil))
	s, err := c.Session(uid1, os.Getenv("TB_OPERATOR_SESSION"))
	if err != nil {
		t.Fatal(err)
	}
	turn, _ := c.Turn(os.Getenv("TB_OPERATOR_TURN"))
	if s.Blocked || s.Consumed != turn.Reservation {
		t.Fatalf("session: %+v", s)
	}
	mustSession(t, c, uid1, "alpha", "auto", bigBudget, "new-session")
	next := mustSubmit(t, c, uid1, s.ID, s.Revision, "next", "continue")
	if next.PredecessorTurnID != "" {
		t.Fatal("resolved unknown became context")
	}
}

func TestOperatorResolutionForgedRecords(t *testing.T) {
	for _, mutation := range []string{"uid", "baseline", "workflow", "digest", "reason", "command", "duplicate"} {
		t.Run(mutation, func(t *testing.T) {
			c, path := newCatalog(t)
			reg := c.st.reg
			turn := linkedTurn(t, c, "forged")
			if err := c.MarkTurnUnknown(turn.ID, "lost"); err != nil {
				t.Fatal(err)
			}
			c.Close()
			if _, err := OperatorResolve(path, reg, turn.ID, "abandon inert work", false, operatorEvidence); err != nil {
				t.Fatal(err)
			}
			j, err := audit.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			trace, _ := j.Trace()
			event := trace.Records[len(trace.Records)-1].Event
			var data map[string]any
			json.Unmarshal(event.Data, &data)
			switch mutation {
			case "uid":
				data["operator_uid"] = 1000
			case "baseline":
				data["baseline_digest"] = b1
			case "workflow":
				data["workflow_id"] = "wrong"
			case "digest":
				data["evidence_digest"] = b1
			case "reason":
				data["reason"] = ""
			case "command":
				data["command_head"] = dg("command activity")
			}
			event.Data, _ = json.Marshal(data)
			if mutation != "digest" {
				var forged operatorResolvedRecord
				json.Unmarshal(event.Data, &forged)
				forged.EvidenceDigest = forged.digest()
				event.Data, _ = json.Marshal(forged)
			}
			// Replace the final event for mutations; append a second valid record for duplication.
			j.Close()
			if mutation != "duplicate" {
				lines := trace.Records[:len(trace.Records)-1]
				os.Remove(path)
				j, err = audit.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				for _, r := range lines {
					if _, err = j.Append(r.Event); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				j, err = audit.Open(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = j.Append(event); err != nil {
				t.Fatal(err)
			}
			j.Close()
			if opened, err := Open(path, reg); err == nil {
				opened.Close()
				t.Fatal("forged catalog accepted")
			}
		})
	}
}

func TestOperatorResolutionMissingCatalogWritesNothing(t *testing.T) {
	path := privateDir(t) + "/missing.jsonl"
	if _, err := OperatorResolve(path, signedRegistration(t, nil), "missing", "reason", true, operatorEvidence); err == nil {
		t.Fatal("missing catalog accepted")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("missing catalog was created: %v", err)
	}
}
