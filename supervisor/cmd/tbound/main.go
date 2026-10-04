// Command tbound is the supervisor process entry point. Provider registration,
// durable session admission, audit wiring, and a contained executor are not
// configured in this prototype slice, so the executable refuses to start an
// ungoverned session. The dependency-injected supervisor loop below is exercised
// with net.Pipe and a synthetic broker in tests only.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
)

var ErrRuntimeNotConfigured = errors.New("provider broker, durable audit, and contained executor are not configured")

// Broker correlates an adapter proposal against trusted provider-broker state.
// The provider-call ID is correlation evidence only, never authorization.
type Broker interface {
	Correlate(context.Context, protocol.Proposal) (correlation.Decision, error)
}

// Executor is an injection point for an effect implementation. Production
// implementations must add durable single-use authority and containment; this
// slice deliberately provides no such implementation.
type Executor interface {
	Execute(context.Context, protocol.Proposal, gate.Decision) (json.RawMessage, error)
}

type Supervisor struct {
	IPC      *ipc.Server
	Broker   Broker
	Policy   gate.Policy
	Executor Executor
}

func main() {
	fmt.Fprintln(os.Stderr, ErrRuntimeNotConfigured)
	// Do not start a socket or accept proposals until trusted runtime adapters
	// and the durable pre-effect audit boundary are provided by a later slice.
	os.Exit(2)
}

// serveConn is the small composition point used by a future host-owned runtime
// after it has created and bound a session connection.
func serveConn(ctx context.Context, conn net.Conn, bindingToken string, broker Broker, policy gate.Policy, executor Executor) error {
	server, err := ipc.NewServer(conn, bindingToken)
	if err != nil {
		return err
	}
	return (&Supervisor{IPC: server, Broker: broker, Policy: policy, Executor: executor}).Serve(ctx)
}

// Serve processes proposals sequentially. Correlation always precedes gate
// evaluation; denied proposals never reach the executor. Any transport,
// broker, gate-result encoding, or executor error closes the stream.
func (s *Supervisor) Serve(ctx context.Context) error {
	if s == nil || s.IPC == nil || s.Broker == nil || s.Executor == nil || s.Policy.Digest() == "" {
		if s != nil && s.IPC != nil {
			_ = s.IPC.Close()
		}
		return errors.New("invalid supervisor session configuration")
	}
	stopClose := context.AfterFunc(ctx, func() { _ = s.IPC.Close() })
	defer stopClose()
	defer s.IPC.Close()
	for {
		proposal, err := s.IPC.ReceiveProposal()
		if errors.Is(err, ipc.ErrClosed) {
			if ctx.Err() != nil {
				return nil
			}
			return nil
		}
		if err != nil {
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
		if decision.Verdict == gate.Allow {
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
		if err := s.IPC.SendResult(result); err != nil {
			return fmt.Errorf("send IPC result: %w", err)
		}
	}
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
