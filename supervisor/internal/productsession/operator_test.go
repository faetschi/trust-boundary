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

// A nonempty command journal is accepted only with the composition's claim that every command settled.
func TestOperatorResolutionAcceptsASettledCommandJournalOnly(t *testing.T) {
	for _, settled := range []bool{false, true} {
		c, path := newCatalog(t)
		reg := c.st.reg
		turn := linkedTurn(t, c, "settled")
		if err := c.MarkTurnUnknown(turn.ID, "lost"); err != nil {
			t.Fatal(err)
		}
		c.Close()
		_, err := OperatorResolve(path, reg, turn.ID, "abandon settled work", false, func(turn Turn) (OperatorEvidence, error) {
			evidence, _ := operatorEvidence(turn)
			evidence.CommandHead, evidence.CommandsSettled = dg("command settlements"), settled
			return evidence, nil
		})
		if (err == nil) != settled {
			t.Fatalf("settled=%v: %v", settled, err)
		}
		if settled {
			opened, err := Open(path, reg)
			if err != nil {
				t.Fatalf("the settled resolution did not replay: %v", err)
			}
			opened.Close()
		}
	}
}

// Settle closes durable evidence of the composition and runs only for a real resolution, after every check and
// before the append. A Settle failure writes no resolution, and the record commits the records it closed.
func TestOperatorResolutionSettlesBeforeAppendOnlyWhenReal(t *testing.T) {
	for _, mode := range []string{"dry-run", "real", "settle-fails", "too-many-closed"} {
		t.Run(mode, func(t *testing.T) {
			c, path := newCatalog(t)
			reg := c.st.reg
			turn := linkedTurn(t, c, "settle")
			if err := c.MarkTurnUnknown(turn.ID, "lost"); err != nil {
				t.Fatal(err)
			}
			c.Close()
			before, _ := os.ReadFile(path)
			settled := 0
			closed := []string{"root-pending-record"}
			if mode == "too-many-closed" {
				closed = make([]string, maxPublicationClosed+1)
				for i := range closed {
					closed[i] = "root-pending-record"
				}
			}
			_, err := OperatorResolve(path, reg, turn.ID, "close the pending publication", mode == "dry-run", func(turn Turn) (OperatorEvidence, error) {
				evidence, _ := operatorEvidence(turn)
				evidence.PublicationClosed = closed
				evidence.Settle = func() error {
					settled++
					if mode == "settle-fails" {
						return os.ErrInvalid
					}
					return nil
				}
				return evidence, nil
			})
			after, _ := os.ReadFile(path)
			switch mode {
			case "dry-run":
				if err != nil || settled != 0 || string(after) != string(before) {
					t.Fatalf("dry run: settled=%d err=%v", settled, err)
				}
			case "real":
				if err != nil || settled != 1 {
					t.Fatalf("real: settled=%d err=%v", settled, err)
				}
				opened, err := Open(path, reg)
				if err != nil {
					t.Fatalf("the resolution did not replay: %v", err)
				}
				resolved, _ := opened.Turn(turn.ID)
				opened.Close()
				if resolved.OperatorResolution == nil || len(resolved.OperatorResolution.PublicationClosed) != 1 {
					t.Fatalf("the record lost the closed publication: %+v", resolved.OperatorResolution)
				}
			default:
				if err == nil || settled > 1 || string(after) != string(before) {
					t.Fatalf("%s: settled=%d err=%v wrote=%v", mode, settled, err, string(after) != string(before))
				}
			}
		})
	}
}

// The catalog hands out copies of its state. The closed-publication list of an operator resolution must not alias it,
// or an edit of a returned turn would change later reads without a journal record.
func TestOperatorResolutionEvidenceDoesNotAliasCatalogState(t *testing.T) {
	c, path := newCatalog(t)
	reg := c.st.reg
	turn := linkedTurn(t, c, "alias")
	if err := c.MarkTurnUnknown(turn.ID, "lost"); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, err := OperatorResolve(path, reg, turn.ID, "close the pending publication", false, func(turn Turn) (OperatorEvidence, error) {
		evidence, _ := operatorEvidence(turn)
		evidence.PublicationClosed = []string{"root-pending-record"}
		return evidence, nil
	}); err != nil {
		t.Fatal(err)
	}
	opened := openAt(t, path, reg)
	read, _ := opened.Turn(turn.ID)
	read.OperatorResolution.PublicationClosed[0] = "edited by the caller"
	again, _ := opened.Turn(turn.ID)
	if got := again.OperatorResolution.PublicationClosed; len(got) != 1 || got[0] != "root-pending-record" {
		t.Fatalf("an edit of a returned turn reached the catalog: %q", got)
	}
}
