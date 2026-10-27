//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"

	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
)

func TestProductServiceHostRefusesWritableManagedAncestry(t *testing.T) {
	for _, target := range []string{"root", "ancestor", "foreign root", "foreign ancestor", "symlink", "outside state"} {
		t.Run(target, func(t *testing.T) {
			f := newProductHostFixture(t, false)
			path := f.projectDir
			if strings.Contains(target, "ancestor") {
				path = filepath.Dir(filepath.Dir(path))
			}
			switch target {
			case "foreign root", "foreign ancestor":
				spoofProductDirectoryOwner(t, path).Store(true)
			case "symlink":
				path = filepath.Dir(filepath.Dir(f.projectDir))
				if err := os.Rename(path, path+"-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-real", path); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(path); _ = os.Rename(path+"-real", path) })
			case "outside state":
				f.cfg.StateRoot = filepath.Join(f.root, "other-state")
				if err := os.Mkdir(f.cfg.StateRoot, 0700); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Chmod(path, 0770); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0700) })
			}
			opts := f.options()
			opts.Host = newProductServiceHostProfile(f.cfg)
			svc, err := startProductService(f.cfg, opts)
			if err == nil {
				_ = svc.Shutdown()
				t.Fatal("a writable managed project ancestry reached a listening socket")
			}
			if target != "root" && !strings.Contains(err.Error(), "managed project exclusivity") {
				t.Fatalf("wrong refusal: %v", err)
			}
			if _, err := os.Stat(f.socketPath); !os.IsNotExist(err) {
				t.Fatal("a refused service opened its socket")
			}
		})
	}
}

func TestProductServiceHostRefusesPublicationWithoutExclusiveManagedAncestry(t *testing.T) {
	for _, target := range []string{"root", "ancestor", "state", "foreign ancestor"} {
		t.Run(target, func(t *testing.T) {
			f := newProductHostFixture(t, false)
			provider := newProductFakeProvider(t, []providerbridge.ScriptTurn{
				{ResponseID: "exclusive-write", CallID: "exclusive-call", Tool: "write", Arguments: json.RawMessage(`{"path":"notes.txt","content":"candidate\n"}`)},
				{ResponseID: "exclusive-final", Text: "Written."},
			})
			routeProductProvider(t, f, provider)
			var foreignOwner *atomic.Bool
			if target == "foreign ancestor" {
				foreignOwner = spoofProductDirectoryOwner(t, filepath.Dir(filepath.Dir(f.projectDir)))
			}
			opts := f.options()
			opts.Host = newProductServiceHostProfile(f.cfg)
			logs := &bytes.Buffer{}
			opts.Log = logs
			f.start(opts)
			session := *f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpCreateSession, CreateSession: &productprotocol.CreateSession{
				ProjectID: productDriverProject, PublicationMode: productsession.PublicationReview, RequestKey: "exclusive-create"}}).Session
			turn := *f.mustCall(f.principalA, f.submitRequest(session, "exclusive-turn", "Write the notes.")).Turn
			events, _ := f.observeUntil(f.principalA, session.ID, 0, kindIs(productprotocol.EventTurnAwaitingReview))
			path := f.projectDir
			if target == "ancestor" {
				path = filepath.Dir(filepath.Dir(path))
			}
			if target == "state" {
				path = f.stateRoot
			}
			if foreignOwner != nil {
				foreignOwner.Store(true)
			} else {
				if err := os.Chmod(path, 0770); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(path, 0700)
			}
			current := f.getSession(f.principalA, session.ID)
			resolved := f.mustCall(f.principalA, productprotocol.Request{Op: productprotocol.OpResolveTurn, ResolveTurn: &productprotocol.ResolveTurn{
				SessionID: session.ID, TurnID: turn.ID, ExpectedRevision: current.Revision, RequestKey: "exclusive-publish",
				CandidateDigest: events[len(events)-1].CandidateDigest, Decision: productsession.DecisionPublish}})
			if resolved.Turn.Outcome == productsession.OutcomePublished || f.notesPublished("notes.txt") {
				t.Fatalf("publication wrote without exclusive managed ancestry: %+v", resolved.Turn)
			}
			if target != "root" && !strings.Contains(logs.String(), "managed project exclusivity") {
				t.Fatalf("wrong refusal: %s", logs.String())
			}
			if fileContent(t, f.projectDir, "task.txt") != "turn fixture\n" {
				t.Fatal("publication changed the baseline")
			}
		})
	}
}

// Ownership is injected at fstat so these refusal tests need neither root nor chown privileges.
func spoofProductDirectoryOwner(t *testing.T, path string) *atomic.Bool {
	t.Helper()
	var target unix.Stat_t
	if err := unix.Stat(path, &target); err != nil {
		t.Fatal(err)
	}
	enabled := &atomic.Bool{}
	original := productManagedRootStat
	productManagedRootStat = func(fd int, st *unix.Stat_t) error {
		err := original(fd, st)
		if err == nil && enabled.Load() && st.Dev == target.Dev && st.Ino == target.Ino {
			st.Uid = uint32(os.Geteuid()) + 1
		}
		return err
	}
	t.Cleanup(func() { productManagedRootStat = original })
	return enabled
}
