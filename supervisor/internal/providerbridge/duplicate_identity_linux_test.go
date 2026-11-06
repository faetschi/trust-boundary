//go:build linux

package providerbridge

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
)

// A provider response whose identity the conversation already holds (a reused response ID, or a tool-call ID of the
// imported predecessor) is raw evidence of a failed exchange: one provider_exchange_unknown record, no
// provider_exchange record, spend left uncertain, conversation closed.
func TestReusedProviderIdentityIsJournaledAsAnUnknownExchange(t *testing.T) {
	for _, tc := range []struct {
		name    string
		callID  string
		prepare func(*Conversation)
		want    string
	}{
		{name: "duplicate response ID", prepare: func(c *Conversation) { c.responseIDs["resp-1"] = struct{}{} },
			want: "provider response ID was reused across conversation turns"},
		{name: "reused imported tool-call ID", callID: "call-imported",
			prepare: func(c *Conversation) { c.reservedCallIDs["call-imported"] = struct{}{} },
			want:    "provider tool-call ID reuses an imported predecessor call"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, appendFails := range []bool{false, true} {
				t.Run(map[bool]string{false: "journaled", true: "append fails"}[appendFails], func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "provider.audit")
					journal := openRecoveryTestJournal(t, path)
					doer := &spendDoer{callID: tc.callID}
					spend := &spendRecorder{doer: doer}
					cfg := recoveryTestConfig()
					cfg.Route, cfg.Spend = spendTestRoute(t), spend
					c, err := newWithDoer(cfg, spendTestModel, doer, journal)
					if err != nil {
						t.Fatal(err)
					}
					if appendFails {
						c.journal = failingKindJournal{journal, "provider_exchange_unknown"}
					}
					tc.prepare(c)
					admitSpendPrompt(t, c)
					_, err = c.NextTurn(context.Background())
					if err == nil || !strings.Contains(err.Error(), tc.want) || !errors.Is(err, ErrClosed) {
						t.Fatalf("NextTurn = %v, want the closed refusal %q", err, tc.want)
					}
					if appendFails != strings.Contains(err.Error(), "fixture journal sync failure") {
						t.Fatalf("append failure surfaced=%v, want %v: %v", !appendFails, appendFails, err)
					}
					if got, want := spend.sequence(), []string{"reserve", "uncertain:unit-1"}; !reflect.DeepEqual(got, want) {
						t.Fatalf("spend order %v, want %v (no settle, no refund)", got, want)
					}
					if !c.Closed() {
						t.Fatal("conversation stayed open")
					}
					trace, err := journal.Trace()
					if err != nil {
						t.Fatal(err)
					}
					var kinds []string
					for _, record := range trace.Records {
						kinds = append(kinds, record.Event.Kind)
					}
					wantKinds := []string{"provider_user_admitted", "provider_exchange_unknown"}
					if appendFails {
						wantKinds = wantKinds[:1]
					}
					if !reflect.DeepEqual(kinds, wantKinds) {
						t.Fatalf("journal kinds %v, want %v", kinds, wantKinds)
					}
					if !appendFails {
						_, err = ReplayTranscript(journal, RecoveryPins{Config: cfg, Model: spendTestModel, Policy: recoveryTestPolicy(t, true)}, &replayVerifierFixture{})
						if !errors.Is(err, ErrTranscriptReplay) || !strings.Contains(err.Error(), "unresolved UNKNOWN") {
							t.Fatalf("replay of the journal = %v, want the unresolved-UNKNOWN refusal", err)
						}
					}
				})
			}
		})
	}
}

// failingKindJournal refuses to append one record kind and passes the rest to the real journal.
type failingKindJournal struct {
	eventJournal
	kind string
}

func (j failingKindJournal) Append(event audit.Event) (audit.Record, error) {
	if event.Kind == j.kind {
		return audit.Record{}, errors.New("fixture journal sync failure")
	}
	return j.eventJournal.Append(event)
}
