// Command tbound is the supervisor process entry point. Production provider
// registration, durable session admission, audit wiring, and a contained
// executor are not configured, so only the explicitly synthetic, no-effect
// Unix-socket smoke session can be started.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"

	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
)

var ErrRuntimeNotConfigured = errors.New("provider broker, durable audit, and contained executor are not configured")

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
// implementations must add durable single-use authority and containment; this
// slice deliberately provides no such implementation.
type Executor interface {
	Execute(context.Context, protocol.Proposal, gate.Decision) (json.RawMessage, error)
}

// DecisionRecorder durably records a gate decision before any effect runs. Serve
// calls it for every verdict, including DENY, so both authorization and refusal
// are durable pre-effect. It is optional: a nil recorder disables recording. A
// returned error is fail-closed and withholds the result.
type DecisionRecorder interface {
	RecordDecision(context.Context, protocol.Proposal, gate.Decision) error
}

type Supervisor struct {
	IPC           *ipc.Server
	Broker        Broker
	Policy        gate.Policy
	Decisions     DecisionRecorder
	Executor      Executor
	Transcript    io.Writer
	ProposalLimit uint64

	handledProposals uint64
	transcriptBytes  uint64
	transcriptEvents uint64
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(args []string, transcript io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound", flag.ContinueOnError)
	flags.SetOutput(stderr)
	smokeListen := flags.Bool("smoke-listen", false, "run the synthetic, no-effect Unix-socket integration listener")
	socketDir := flags.String("socket-dir", "", "caller-created private mode-0700 directory for the smoke socket")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if !*smokeListen {
		return ErrRuntimeNotConfigured
	}
	if runtime.GOOS != "linux" {
		return errors.New("the synthetic Unix-socket smoke listener is available only on Linux")
	}
	if *socketDir == "" {
		return errors.New("--socket-dir is required for --smoke-listen")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runSyntheticListener(ctx, *socketDir, transcript)
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
	if s == nil || s.IPC == nil || s.Broker == nil || s.Executor == nil || s.Policy.Digest() == "" ||
		s.ProposalLimit > ipc.MaxFramesPerDirection {
		if s != nil && s.IPC != nil {
			_ = s.IPC.Close()
		}
		return errors.New("invalid supervisor session configuration")
	}
	stopClose := context.AfterFunc(ctx, func() { _ = s.IPC.Close() })
	defer stopClose()
	defer s.IPC.Close()
	for {
		if s.ProposalLimit > 0 && s.handledProposals >= s.ProposalLimit {
			return nil
		}
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
		if s.Decisions != nil {
			if err := s.Decisions.RecordDecision(ctx, proposal, decision); err != nil {
				_ = s.IPC.Close()
				return fmt.Errorf("record gate decision; result withheld: %w", err)
			}
		}
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
