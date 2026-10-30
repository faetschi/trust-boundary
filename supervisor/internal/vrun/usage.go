package vrun

import (
	"bytes"
	"encoding/json"
	"math"
)

// MaxLineBytes bounds one line of Pi's event stream that the scanner parses. A longer line is
// skipped (its usage is not counted) and counted in Usage.Skipped.
const MaxLineBytes = 16 << 20

// Usage is the sum of the usage Pi reported in `--mode json`.
type Usage struct {
	TokensIn     int64
	TokensOut    int64
	CostMicroUSD int64
	ToolCalls    int
	// Events is the number of completed assistant messages summed.
	Events int
	// Skipped is the number of lines that were too long or were not valid JSON where they had to be.
	Skipped int
}

// UsageScanner is an io.Writer that reads Pi's `--mode json` stream (strict JSONL, LF framed)
// while it is written and sums the usage of every completed assistant message (a message_end
// event) and the number of finished tool executions (tool_execution_end). It keeps at most one
// partial line, so a long run costs no memory. Only the final message_end carries the
// authoritative usage, so message_update events are ignored.
type UsageScanner struct {
	partial []byte
	drop    bool // the current line is over MaxLineBytes: discard until its LF
	usage   Usage
}

// Usage returns the totals so far. A last line without its LF is not counted until Flush.
func (s *UsageScanner) Usage() Usage { return s.usage }

// Write implements io.Writer. It never fails.
func (s *UsageScanner) Write(p []byte) (int, error) {
	rest := p
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			s.append(rest)
			break
		}
		s.append(rest[:i])
		s.endLine()
		rest = rest[i+1:]
	}
	return len(p), nil
}

// Flush accounts for a last line that has no LF.
func (s *UsageScanner) Flush() {
	if len(s.partial) > 0 || s.drop {
		s.endLine()
	}
}

func (s *UsageScanner) append(b []byte) {
	if s.drop {
		return
	}
	if len(s.partial)+len(b) > MaxLineBytes {
		s.drop, s.partial = true, nil
		return
	}
	s.partial = append(s.partial, b...)
}

var (
	messageEndMarker = []byte(`"message_end"`)
	toolEndMarker    = []byte(`"tool_execution_end"`)
)

func (s *UsageScanner) endLine() {
	line := bytes.TrimSuffix(s.partial, []byte("\r"))
	dropped := s.drop
	defer func() { s.partial, s.drop = s.partial[:0], false }()
	if dropped {
		s.usage.Skipped++
		return
	}
	switch {
	case bytes.Contains(line, messageEndMarker):
		s.messageEnd(line)
	case bytes.Contains(line, toolEndMarker):
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &event) == nil && event.Type == "tool_execution_end" {
			s.usage.ToolCalls++
		}
	}
}

func (s *UsageScanner) messageEnd(line []byte) {
	var event struct {
		Type    string `json:"type"`
		Message struct {
			Role  string `json:"role"`
			Usage *struct {
				Input      float64 `json:"input"`
				Output     float64 `json:"output"`
				CacheRead  float64 `json:"cacheRead"`
				CacheWrite float64 `json:"cacheWrite"`
				Cost       struct {
					Total float64 `json:"total"`
				} `json:"cost"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		s.usage.Skipped++
		return
	}
	if event.Type != "message_end" || event.Message.Role != "assistant" || event.Message.Usage == nil {
		return
	}
	u := event.Message.Usage
	s.usage.TokensIn += count(u.Input) + count(u.CacheRead) + count(u.CacheWrite)
	s.usage.TokensOut += count(u.Output)
	s.usage.CostMicroUSD += count(u.Cost.Total * 1e6)
	s.usage.Events++
}

// count rounds a reported number to a non-negative integer; NaN and infinities count as zero.
func count(v float64) int64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0
	}
	return int64(math.Round(v))
}
