package webview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const previewBytes = 256

// Source names one read-only JSONL file. Kind is "audit" for audit.Record
// lines or "transcript" for supervisor transcript entries.
type Source struct {
	Name string
	Path string
	Kind string
}

// TailerOptions controls polling and per-line memory bounds. Zero values use
// the package defaults.
type TailerOptions struct {
	PollInterval time.Duration
	MaxLineBytes int
}

type fileState struct {
	source      Source
	file        *os.File
	info        os.FileInfo
	offset      int64
	pending     []byte
	dropping    bool
	preview     []byte
	droppedByte int64
}

// Tailer follows configured files from their current end at startup. It reads
// each file with os.Open (read-only); it never creates, truncates, or writes a
// source file. Replacements and truncations are reopened and read from the new
// beginning so subsequent complete lines remain observable.
type Tailer struct {
	states  []*fileState
	buffer  *Buffer
	options TailerOptions
	started bool
	done    chan error
}

// NewTailer creates a tailer but does not open any paths until Start.
func NewTailer(sources []Source, buffer *Buffer, options TailerOptions) (*Tailer, error) {
	if buffer == nil {
		return nil, errors.New("webview tailer requires a record buffer")
	}
	if options.PollInterval <= 0 {
		options.PollInterval = DefaultPollInterval
	}
	if options.MaxLineBytes <= 0 {
		options.MaxLineBytes = DefaultMaxLineBytes
	}
	states := make([]*fileState, 0, len(sources))
	for _, source := range sources {
		if strings.TrimSpace(source.Path) == "" {
			return nil, errors.New("webview source path must not be empty")
		}
		if strings.TrimSpace(source.Name) == "" {
			source.Name = source.Path
		}
		states = append(states, &fileState{source: source})
	}
	return &Tailer{states: states, buffer: buffer, options: options}, nil
}

// Start opens all configured files read-only, positions each at its current
// end, then begins polling until ctx is canceled or a read fails.
func (t *Tailer) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("webview tailer requires a context")
	}
	if t.started {
		return errors.New("webview tailer has already been started")
	}
	for _, state := range t.states {
		if err := openState(state, false); err != nil {
			t.closeStates("tailer stopped during startup")
			return fmt.Errorf("open %s %q read-only: %w", state.source.Kind, state.source.Path, err)
		}
	}
	t.started = true
	t.done = make(chan error, 1)
	go func() {
		t.done <- t.run(ctx)
		close(t.done)
	}()
	return nil
}

// Done returns the channel which receives the tailer's termination error.
func (t *Tailer) Done() <-chan error { return t.done }

func (t *Tailer) run(ctx context.Context) error {
	ticker := time.NewTicker(t.options.PollInterval)
	defer ticker.Stop()
	defer t.closeStates("tailer stopped before the line ended")
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			for _, state := range t.states {
				if err := t.poll(state); err != nil {
					return fmt.Errorf("tail %q: %w", state.source.Path, err)
				}
			}
		}
	}
}

func openState(state *fileState, fromStart bool) error {
	// os.Open is intentionally used instead of OpenFile with write-capable flags.
	file, err := os.Open(state.source.Path)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return errors.New("source is not a regular file")
	}
	position := int64(0)
	if !fromStart {
		position, err = file.Seek(0, io.SeekEnd)
	} else {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err != nil {
		_ = file.Close()
		return err
	}
	state.file = file
	state.info = info
	state.offset = position
	state.pending = nil
	state.dropping = false
	state.preview = nil
	state.droppedByte = 0
	return nil
}

func (t *Tailer) poll(state *fileState) error {
	if state.file == nil {
		if err := openState(state, true); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
	}
	if err := t.readAvailable(state); err != nil {
		return err
	}
	pathInfo, err := os.Stat(state.source.Path)
	if errors.Is(err, os.ErrNotExist) {
		// Keep the old descriptor while a rotated path is temporarily absent.
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(state.info, pathInfo) {
		t.finishPartial(state, "file was rotated before the line ended")
		if err := state.file.Close(); err != nil {
			return err
		}
		state.file = nil
		if err := openState(state, true); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		return t.readAvailable(state)
	}
	if pathInfo.Size() < state.offset {
		t.finishPartial(state, "file was truncated before the line ended")
		if err := state.file.Close(); err != nil {
			return err
		}
		state.file = nil
		if err := openState(state, true); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		return t.readAvailable(state)
	}
	return nil
}

func (t *Tailer) readAvailable(state *fileState) error {
	var chunk [32 * 1024]byte
	for {
		count, err := state.file.Read(chunk[:])
		if count > 0 {
			state.offset += int64(count)
			t.consume(state, chunk[:count])
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
	}
}

func (t *Tailer) consume(state *fileState, chunk []byte) {
	for _, current := range chunk {
		if state.dropping {
			if current == '\n' {
				t.publishOversized(state)
				state.dropping = false
				state.preview = nil
				state.droppedByte = 0
			} else if state.droppedByte < int64(^uint64(0)>>1) {
				state.droppedByte++
			}
			continue
		}
		if current == '\n' {
			line := state.pending
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			t.publishLine(state.source, line)
			state.pending = nil
			continue
		}
		if len(state.pending) < t.options.MaxLineBytes {
			state.pending = append(state.pending, current)
			continue
		}
		state.preview = append(state.preview, state.pending[:min(len(state.pending), previewBytes)]...)
		if len(state.preview) > previewBytes {
			state.preview = state.preview[:previewBytes]
		}
		if len(state.preview) < previewBytes {
			state.preview = append(state.preview, current)
		}
		state.dropping = true
		state.droppedByte = int64(len(state.pending) + 1)
		state.pending = nil
	}
}

func (t *Tailer) publishOversized(state *fileState) {
	record := malformedRecord(state.source, string(state.preview), fmt.Sprintf(
		"line exceeds the configured maximum of %d bytes (at least %d bytes)",
		t.options.MaxLineBytes, state.droppedByte,
	))
	t.buffer.publish(record)
}

func (t *Tailer) publishLine(source Source, line []byte) {
	var parsed json.RawMessage
	if err := json.Unmarshal(line, &parsed); err != nil {
		t.buffer.publish(malformedRecord(source, string(line), "invalid JSON: "+err.Error()))
		return
	}
	t.buffer.publish(Record{
		Source: source.Name, Types: classify(source, parsed),
		ReceivedAt: time.Now().UTC(), Record: parsed,
	})
}

func malformedRecord(source Source, line, reason string) Record {
	types := []string{"malformed"}
	if source.Kind == "audit" {
		types = append(types, "audit")
	}
	return Record{
		Source: source.Name, Types: types, ReceivedAt: time.Now().UTC(),
		RawLine: line, ParseError: reason,
	}
}

func classify(source Source, parsed json.RawMessage) []string {
	if source.Kind == "audit" {
		return []string{"audit"}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(parsed, &object); err != nil || object == nil {
		return []string{"record"}
	}
	types := make([]string, 0, 4)
	for _, name := range []string{"proposal", "correlation", "decision", "result"} {
		if _, ok := object[name]; ok {
			types = append(types, name)
		}
	}
	if len(types) != 0 {
		return types
	}
	var explicitType string
	if raw, ok := object["type"]; ok && json.Unmarshal(raw, &explicitType) == nil {
		switch explicitType {
		case "proposal", "correlation", "decision", "result", "audit":
			return []string{explicitType}
		}
	}
	return []string{"record"}
}

func (t *Tailer) finishPartial(state *fileState, reason string) {
	if state.dropping {
		record := malformedRecord(state.source, string(state.preview), fmt.Sprintf(
			"line exceeds the configured maximum of %d bytes and %s",
			t.options.MaxLineBytes, reason,
		))
		t.buffer.publish(record)
	} else if len(state.pending) != 0 {
		line := bytes.TrimSuffix(state.pending, []byte{'\r'})
		t.buffer.publish(malformedRecord(state.source, string(line), "incomplete JSONL line: "+reason))
	}
	state.pending = nil
	state.preview = nil
	state.dropping = false
	state.droppedByte = 0
}

func (t *Tailer) closeStates(reason string) {
	for _, state := range t.states {
		if state.file == nil {
			continue
		}
		t.finishPartial(state, reason)
		_ = state.file.Close()
		state.file = nil
	}
}
