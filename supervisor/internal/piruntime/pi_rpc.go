package piruntime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"tbound/supervisor/internal/broker/protocol"
)

const PiRPCMaxRecordBytes = 1 << 20
const (
	piRPCMaxIdentityBytes = 256
	piRPCMaxPromptBytes   = 16 << 10
)

// PiRPCRecord is one official Pi CLI JSONL record. The CLI owns the agent
// loop; TBound correlates command responses and maps lifecycle events around
// its one admitted active prompt.
type PiRPCRecord struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Command string          `json:"command,omitempty"`
	Success *bool           `json:"success,omitempty"`
	Error   string          `json:"error,omitempty"`
	Raw     json.RawMessage `json:"-"`
}

type PiRPCClient struct {
	reader         *bufio.Reader
	writer         io.Writer
	writeMu        sync.Mutex
	inputClosed    atomic.Bool
	inputCloseOnce sync.Once
	inputCloseErr  error
}

func NewPiRPCClient(reader io.Reader, writer io.Writer) (*PiRPCClient, error) {
	if reader == nil || writer == nil {
		return nil, errors.New("Pi RPC client requires process stdin and stdout")
	}
	return &PiRPCClient{reader: bufio.NewReaderSize(reader, 64<<10), writer: writer}, nil
}

// SendPrompt sends exactly one ID-correlated prompt command as required by Pi
// 0.87.1 RPC mode. It does not infer completion from the prompt response.
func (c *PiRPCClient) SendPrompt(id, message string) error {
	if c == nil || !piRPCValidIdentity(id, piRPCMaxIdentityBytes) || !piRPCValidPrompt(message) {
		return errors.New("Pi RPC prompt identity or message is invalid")
	}
	return c.writeRecord(struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}{id, "prompt", message})
}

func (c *PiRPCClient) Abort(id string) error {
	if c == nil || !piRPCValidIdentity(id, piRPCMaxIdentityBytes) {
		return errors.New("Pi RPC abort identity is invalid")
	}
	return c.writeRecord(struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}{id, "abort"})
}

func (c *PiRPCClient) CloseInput() error {
	if c == nil {
		return nil
	}
	// Close must interrupt a blocked write, so it cannot acquire writeMu.
	c.inputCloseOnce.Do(func() {
		c.inputClosed.Store(true)
		closer, ok := c.writer.(io.Closer)
		if !ok {
			c.inputCloseErr = errors.New("Pi RPC input does not support orderly close")
			return
		}
		c.inputCloseErr = closer.Close()
	})
	return c.inputCloseErr
}

func (c *PiRPCClient) writeRecord(value any) error {
	body, err := json.Marshal(value)
	if err != nil || len(body) == 0 || len(body) > PiRPCMaxRecordBytes {
		return errors.New("Pi RPC command exceeds its JSONL record bound")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.inputClosed.Load() {
		return errors.New("Pi RPC input is closed")
	}
	if err := writeRPCBytes(c.writer, append(body, '\n')); err != nil {
		return fmt.Errorf("write Pi RPC command: %w", err)
	}
	return nil
}

func writeRPCBytes(writer io.Writer, body []byte) error {
	for len(body) > 0 {
		written, err := writer.Write(body)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		body = body[written:]
	}
	return nil
}

// ReadRecord reads a single LF-delimited Pi RPC record. CRLF is accepted, but
// Unicode line separators inside JSON strings remain ordinary data.
func (c *PiRPCClient) ReadRecord() (PiRPCRecord, error) {
	if c == nil || c.reader == nil {
		return PiRPCRecord{}, errors.New("Pi RPC output is unavailable")
	}
	var body []byte
	for {
		part, err := c.reader.ReadSlice('\n')
		if len(body)+len(part) > PiRPCMaxRecordBytes+1 {
			return PiRPCRecord{}, errors.New("Pi RPC output record exceeds its byte bound")
		}
		body = append(body, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return PiRPCRecord{}, err
		}
		break
	}
	body = bytes.TrimSuffix(body, []byte{'\n'})
	body = bytes.TrimSuffix(body, []byte{'\r'})
	if len(body) == 0 || len(body) > PiRPCMaxRecordBytes {
		return PiRPCRecord{}, errors.New("Pi RPC output record has an invalid length")
	}
	if err := protocol.ValidateStrictJSON(body); err != nil {
		return PiRPCRecord{}, fmt.Errorf("Pi RPC output is not strict JSON: %w", err)
	}
	var record PiRPCRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return PiRPCRecord{}, fmt.Errorf("decode Pi RPC record: %w", err)
	}
	if !validPiRPCRecord(record) {
		return PiRPCRecord{}, errors.New("Pi RPC output record has an invalid type or command response")
	}
	record.Raw = append(json.RawMessage(nil), body...)
	return record, nil
}

func validPiRPCRecord(record PiRPCRecord) bool {
	if record.Type == "response" {
		return piRPCValidIdentity(record.ID, piRPCMaxIdentityBytes) &&
			piRPCValidIdentity(record.Command, 64) && record.Success != nil &&
			(*record.Success || strings.TrimSpace(record.Error) != "")
	}
	return piRPCValidIdentity(record.Type, 64)
}

func piRPCValidIdentity(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func piRPCValidPrompt(value string) bool {
	return strings.TrimSpace(value) != "" && len([]byte(value)) <= piRPCMaxPromptBytes
}
