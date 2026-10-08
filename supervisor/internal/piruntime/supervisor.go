package piruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
)

const (
	maxTranscriptEntries = 4
	maxTranscriptBytes   = 8 << 20
)

// Broker correlates an adapter proposal against trusted provider-broker state.
// The provider-call ID is correlation evidence only, never authorization.
type Broker interface {
	Correlate(context.Context, protocol.Proposal) (correlation.Decision, error)
}

// Executor is an injection point for an effect implementation. Production
// implementations must add durable single-use authority and containment.
type Executor interface {
	Execute(context.Context, protocol.Proposal, gate.Decision) (json.RawMessage, error)
}

// DurableDecisionRecorder is the production composition contract to persist a
// gate decision before any effect runs. Effect-capable Serve calls it for every
// verdict, including DENY. A returned error is
// fail-closed and withholds the result. The interface type is a composition
// requirement, not proof that an implementation is durable or trusted.
type DurableDecisionRecorder interface {
	RecordDecision(context.Context, protocol.Proposal, gate.Decision) error
}

// DecisionRecorder is retained as the source-compatible name.
type DecisionRecorder = DurableDecisionRecorder

// NoEffectSession is an explicit synthetic transport/gate fixture mode. When
// present, Serve requires Executor and Decisions to be nil and never invokes an
// effect callback; ALLOW decisions return only a fixed non-effect receipt. It
// does not represent a durable decision record or authorize production use.
type NoEffectSession struct{}

// Supervisor is the shared native/adapter proposal-result loop. Its exported
// fields intentionally retain the original cmd/tbound construction contract.
// A host composition root supplies a session-bound IPC endpoint (and any peer
// authentication separately),
// registered broker, immutable policy, and durable executor.
type Supervisor struct {
	IPC           *ipc.Server
	Broker        Broker
	Policy        gate.Policy
	Decisions     DurableDecisionRecorder
	Executor      Executor
	NoEffect      *NoEffectSession
	Transcript    io.Writer
	ProposalLimit uint64

	handledProposals uint64
	transcriptBytes  uint64
	transcriptEvents uint64
}

// HandledProposals returns the number of proposals for which a result was sent.
func (s *Supervisor) HandledProposals() uint64 {
	if s == nil {
		return 0
	}
	return s.handledProposals
}

// Serve processes proposals sequentially. Correlation always precedes gate
// evaluation; denied proposals never reach the executor. Any transport,
// broker, gate-result encoding, or executor error closes the stream.
func (s *Supervisor) Serve(ctx context.Context) error {
	if s == nil || s.IPC == nil || s.Broker == nil || s.Policy.Digest() == "" ||
		s.ProposalLimit > ipc.MaxFramesPerDirection {
		if s != nil && s.IPC != nil {
			_ = s.IPC.Close()
		}
		return errors.New("invalid supervisor session configuration")
	}
	noEffect := s.NoEffect != nil
	if (noEffect && (s.Executor != nil || s.Decisions != nil)) ||
		(!noEffect && (isNilBinding(s.Executor) || isNilBinding(s.Decisions))) {
		_ = s.IPC.Close()
		return errors.New("effect-capable supervisor requires a decision recorder and executor; no-effect mode must be explicit and have neither")
	}
	if ctx == nil {
		_ = s.IPC.Close()
		return errors.New("supervisor context is required")
	}
	stopClose := context.AfterFunc(ctx, func() { _ = s.IPC.Close() })
	defer stopClose()
	defer s.IPC.Close()
	for {
		if s.ProposalLimit > 0 && s.handledProposals >= s.ProposalLimit {
			return nil
		}
		proposal, err := s.IPC.ReceiveProposal()
		if err != nil {
			// Cancellation closes the bound transport out from under a blocked
			// read. Treat that as a clean stop so no result is released.
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, ipc.ErrClosed) {
				return nil
			}
			return fmt.Errorf("receive IPC proposal: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return nil
		}
		matched, err := s.Broker.Correlate(ctx, proposal)
		if err != nil {
			return fmt.Errorf("broker correlation failed: %w", err)
		}
		decision := gate.Evaluate(proposal, matched, s.Policy)
		result := resultFor(proposal, decision)
		if !noEffect {
			if err := s.Decisions.RecordDecision(ctx, proposal, decision); err != nil {
				_ = s.IPC.Close()
				return fmt.Errorf("record gate decision; result withheld: %w", err)
			}
		}
		if decision.Verdict == gate.Allow {
			if noEffect {
				result.Output = json.RawMessage(`{"status":"stubbed-no-effect"}`)
			} else {
				output, err := s.Executor.Execute(ctx, proposal, decision)
				if err != nil {
					return fmt.Errorf("executor failed; result withheld: %w", err)
				}
				if len(output) > protocol.MaxFrameBytes {
					return errors.New("executor output exceeds the protocol bound; result withheld")
				}
				if len(output) > 0 {
					if err := protocol.ValidateStrictJSON(output); err != nil {
						return fmt.Errorf("executor output is not bounded strict JSON; result withheld: %w", err)
					}
				}
				result.Output = append(json.RawMessage(nil), output...)
			}
		}
		if err := s.IPC.SendResult(result); err != nil {
			return fmt.Errorf("send IPC result: %w", err)
		}
		s.handledProposals++
		if err := s.writeTranscript(transcriptEntry{
			Proposal: proposal, Correlation: matched, Decision: decision, Result: result,
		}); err != nil {
			return fmt.Errorf("write bounded supervisor transcript: %w", err)
		}
	}
}

type transcriptEntry struct {
	Proposal    protocol.Proposal    `json:"proposal"`
	Correlation correlation.Decision `json:"correlation"`
	Decision    gate.Decision        `json:"decision"`
	Result      protocol.Result      `json:"result"`
}

func (s *Supervisor) writeTranscript(entry transcriptEntry) error {
	if s.Transcript == nil {
		return nil
	}
	if s.transcriptEvents >= maxTranscriptEntries {
		return errors.New("transcript event limit exceeded")
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode transcript event: %w", err)
	}
	if len(encoded)+1 > maxTranscriptBytes || s.transcriptBytes > maxTranscriptBytes-uint64(len(encoded)+1) {
		return errors.New("transcript byte limit exceeded")
	}
	encoded = append(encoded, '\n')
	if err := writeTranscriptBytes(s.Transcript, encoded); err != nil {
		return err
	}
	s.transcriptBytes += uint64(len(encoded))
	s.transcriptEvents++
	return nil
}

func writeTranscriptBytes(writer io.Writer, encoded []byte) error {
	for len(encoded) > 0 {
		written, err := writer.Write(encoded)
		if written < 0 || written > len(encoded) {
			return fmt.Errorf("invalid transcript write count %d", written)
		}
		encoded = encoded[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func resultFor(proposal protocol.Proposal, decision gate.Decision) protocol.Result {
	return protocol.Result{
		SchemaVersion:            protocol.ResultSchemaVersion,
		ResponseID:               decision.ResponseID,
		ToolCallID:               proposal.ToolCallID,
		Tool:                     proposal.Tool,
		Sequence:                 decision.Sequence,
		CanonicalArgumentsDigest: decision.CanonicalArgumentsDigest,
		Verdict:                  string(decision.Verdict),
		ReasonCode:               decision.ReasonCode,
		PolicyDigest:             decision.PolicyDigest,
	}
}
