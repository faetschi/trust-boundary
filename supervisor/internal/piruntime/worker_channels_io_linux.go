//go:build linux

// worker_channels_io_linux.go: Pi worker channel I/O: bootstrap and control frame writes, event reads and decoding.

package piruntime

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"tbound/supervisor/internal/broker/protocol"
)

func (c *PiWorkerChannels) writeBootstrap(profile HostProfile, input PiWorkerBootstrap) error {
	if c == nil || c.bootstrapWriter == nil || c.closed || c.childClosed {
		return errors.New("Pi worker bootstrap channel is unavailable")
	}
	if err := profile.Validate(); err != nil {
		return err
	}
	if !validDigest(input.SourceBindingDigest) || !validDigest(input.SourceProvenanceDigest) ||
		!workerValidIdentity(input.SourceGenerationID, 256) || !validDigest(input.SourceTreeDigest) ||
		!validDigest(input.SourceManifestDigest) || !validDigest(input.WorkerViewIdentity) || input.WorkerMountID == 0 ||
		!workerValidIdentity(input.SessionID, 256) ||
		!workerValidIdentity(input.WorkflowID, 256) || !workerValidIdentity(input.ConversationID, 256) {
		return errors.New("Pi worker bootstrap source/session binding is incomplete")
	}
	wire := piWorkerBootstrapWire{
		SchemaVersion:          PiWorkerSchemaVersion,
		ProfileID:              profile.ID,
		ProfileDigest:          profile.Digest,
		SourceBindingDigest:    input.SourceBindingDigest,
		SourceProvenanceDigest: input.SourceProvenanceDigest,
		SourceGenerationID:     input.SourceGenerationID,
		SourceTreeDigest:       input.SourceTreeDigest,
		SourceManifestDigest:   input.SourceManifestDigest,
		WorkerViewIdentity:     input.WorkerViewIdentity,
		WorkerMountID:          input.WorkerMountID,
		RuntimeBundleDigest:    profile.PiRuntimeBundleDigest,
		DescriptorProfile:      profile.DescriptorProfile,
		DescriptorDigest:       profile.DescriptorProfileDigest,
		ChildEnvironmentDigest: profile.ChildEnvironmentDigest,
		SessionID:              input.SessionID,
		WorkflowID:             input.WorkflowID,
		ConversationID:         input.ConversationID,
		PrivateCWD:             profile.WorkingDirectory,
		IPCBindingToken:        c.bindingToken,
		ProviderID:             PiWorkerProviderID,
		ModelID:                PiWorkerModelID,
		NodeVersion:            profile.NodeVersion,
		PiVersion:              "0.87.1",
		Tools:                  []string{"read", "write", "edit", "bash"},
		ToolSchemaSHA256:       cloneToolSchemaDigests(),
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return fmt.Errorf("encode Pi worker bootstrap: %w", err)
	}
	if len(encoded) == 0 || len(encoded) > 16<<10 {
		return errors.New("Pi worker bootstrap exceeds bound")
	}
	if err := writePiFrame(c.bootstrapWriter, encoded); err != nil {
		return err
	}
	err = c.bootstrapWriter.Close()
	c.bootstrapWriter = nil
	return err
}

func cloneToolSchemaDigests() map[string]string { return piWorkerToolSchemaDigests() }

func writePiFrame(writer io.Writer, body []byte) error {
	if len(body) == 0 || len(body) > PiWorkerMaxFrameBytes {
		return errors.New("Pi worker frame length is invalid")
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
	if err := writePiAll(writer, prefix[:]); err != nil {
		return err
	}
	return writePiAll(writer, body)
}

func writePiAll(writer io.Writer, value []byte) error {
	for len(value) != 0 {
		written, err := writer.Write(value)
		if written < 0 || written > len(value) {
			return errors.New("Pi worker writer returned invalid count")
		}
		value = value[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (c *PiWorkerChannels) sendControl(ctx context.Context, frame PiWorkerControl) error {
	if c == nil || ctx == nil {
		return errors.New("Pi worker control channel is unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.controlWriter == nil {
		return errors.New("Pi worker control channel is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.controlSeq == math.MaxUint64 {
		return errors.New("Pi worker control sequence exhausted")
	}
	frame.Sequence = c.controlSeq
	encoded, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(encoded) == 0 || len(encoded) > PiWorkerMaxFrameBytes {
		return errors.New("Pi worker control frame exceeds bound")
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(encoded)))
	writer := c.controlWriter
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		if err := writer.SetWriteDeadline(deadline); err != nil {
			return err
		}
	}
	stopCancellation := context.AfterFunc(ctx, func() { _ = writer.SetWriteDeadline(time.Now()) })
	defer stopCancellation()
	defer writer.SetWriteDeadline(time.Time{})
	if err := writePiAll(writer, prefix[:]); err != nil {
		var closeErr error
		if c.controlInput != nil {
			closeErr = c.controlInput.Close()
		}
		c.controlWriter = nil
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), err, closeErr)
		}
		return errors.Join(err, closeErr)
	}
	if err := writePiAll(writer, encoded); err != nil {
		var closeErr error
		if c.controlInput != nil {
			closeErr = c.controlInput.Close()
		}
		c.controlWriter = nil
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), err, closeErr)
		}
		return errors.Join(err, closeErr)
	}
	c.controlSeq++
	return nil
}

func (c *PiWorkerChannels) Abort(ctx context.Context, requestID string) error {
	if !workerValidIdentity(requestID, 256) {
		return errors.New("Pi worker abort request ID is invalid")
	}
	return c.sendControl(ctx, PiWorkerControl{Type: "abort", RequestID: requestID})
}

func (c *PiWorkerChannels) NextEvent(ctx context.Context) (PiWorkerEvent, error) {
	if c == nil || ctx == nil {
		return PiWorkerEvent{}, errors.New("Pi worker event stream is unavailable")
	}
	select {
	case event, ok := <-c.events:
		if ok {
			return event, nil
		}
		c.eventsMu.Lock()
		defer c.eventsMu.Unlock()
		if c.eventsDropped > 0 && !c.gapReturned {
			c.gapReturned = true
			return PiWorkerEvent{Type: "observation_gap", DroppedEvents: c.eventsDropped, DroppedBytes: c.eventsBytes}, nil
		}
		if c.eventsErr != nil {
			return PiWorkerEvent{}, c.eventsErr
		}
		return PiWorkerEvent{}, io.EOF
	case <-ctx.Done():
		return PiWorkerEvent{}, ctx.Err()
	}
}

func (c *PiWorkerChannels) readEvents() {
	defer close(c.eventsDone)
	defer close(c.events)
	var totalBytes uint64
	var sequence uint64 = 1
	for count := 0; count < PiWorkerMaxEventCount; count++ {
		var prefix [4]byte
		if _, err := io.ReadFull(c.eventReader, prefix[:]); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
				c.setEventError(fmt.Errorf("read Pi event prefix: %w", err))
			}
			return
		}
		length := binary.BigEndian.Uint32(prefix[:])
		if length == 0 || length > PiWorkerMaxFrameBytes {
			c.setEventError(errors.New("Pi worker event frame length is invalid"))
			return
		}
		body := make([]byte, int(length))
		if _, err := io.ReadFull(c.eventReader, body); err != nil {
			c.setEventError(fmt.Errorf("read Pi worker event: %w", err))
			return
		}
		totalBytes = saturatingAdd(totalBytes, uint64(length))
		if totalBytes > PiWorkerMaxEventBytes {
			c.setEventError(errors.New("Pi worker event stream byte budget exhausted"))
			return
		}
		if err := protocol.ValidateStrictJSON(body); err != nil {
			c.setEventError(fmt.Errorf("worker event is not strict JSON: %w", err))
			return
		}
		event, err := decodePiWorkerEvent(body)
		if err != nil {
			c.setEventError(err)
			return
		}
		if event.Sequence != sequence || sequence == math.MaxUint64 {
			c.setEventError(errors.New("worker event sequence is duplicate, stale, or exhausted"))
			return
		}
		sequence++
		select {
		case c.events <- event:
		default:
			c.eventsMu.Lock()
			c.eventsDropped++
			c.eventsBytes = saturatingAdd(c.eventsBytes, uint64(length))
			c.eventsMu.Unlock()
		}
	}
	c.setEventError(errors.New("Pi worker event frame budget exhausted"))
}

func (c *PiWorkerChannels) setEventError(err error) {
	c.eventsMu.Lock()
	if c.eventsErr == nil {
		c.eventsErr = err
	}
	c.eventsMu.Unlock()
}

func decodePiWorkerEvent(raw []byte) (PiWorkerEvent, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return PiWorkerEvent{}, err
	}
	var event PiWorkerEvent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return event, err
	}
	if event.Sequence == 0 || !workerValidIdentity(event.Type, 32) {
		return event, errors.New("worker event has invalid sequence or type")
	}
	allowed := map[string][]string{
		"ready":        {"type", "sequence", "schema_version", "profile_id", "profile_digest", "source_binding_digest", "source_provenance_digest", "source_generation_id", "source_tree_digest", "source_manifest_digest", "worker_view_identity", "worker_mount_id", "runtime_bundle_digest", "descriptor_profile", "descriptor_profile_digest", "node_version", "pi_version", "provider_id", "model_id", "tools", "tool_schema_sha256"},
		"turn_end":     {"type", "sequence", "request_id"},
		"worker_error": {"type", "sequence", "request_id", "code"},
		"stopped":      {"type", "sequence", "request_id"},
	}
	keys, ok := allowed[event.Type]
	if !ok {
		return event, fmt.Errorf("unsupported Pi worker event type %q", event.Type)
	}
	if len(fields) != len(keys) {
		for _, optional := range []string{"request_id"} {
			if event.Type == "worker_error" || event.Type == "stopped" {
				if len(fields) == len(keys)-1 && fields[optional] == nil {
					break
				}
			}
		}
		if event.Type != "worker_error" && event.Type != "stopped" || len(fields) < len(keys)-1 || len(fields) > len(keys) {
			return event, errors.New("worker event has unexpected or missing fields")
		}
	}
	for _, key := range keys {
		if fields[key] == nil && !(key == "request_id" && (event.Type == "worker_error" || event.Type == "stopped")) {
			return event, fmt.Errorf("worker event is missing %q", key)
		}
	}
	if (event.Type == "turn_end" && !workerValidIdentity(event.RequestID, 256)) ||
		(event.Type == "worker_error" && !workerValidIdentity(event.Code, 64)) ||
		(event.Type == "stopped" && event.RequestID != "" && !workerValidIdentity(event.RequestID, 256)) {
		return event, errors.New("worker lifecycle event has invalid request identity or error code")
	}
	return event, nil
}
