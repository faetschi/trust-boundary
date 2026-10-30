package netoracle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"tbound/supervisor/internal/audit"
)

// IntervalJournal is the part of a guest sink journal that decides one interval: the
// chain from its first record through the interval's end canary. Records after the end
// canary cannot change the verdict (a connection accepted after it is outside the window,
// and the evaluator reads a close record after it as "still open"), so they are not sent.
type IntervalJournal struct {
	Journal []byte // whole records only, a valid chain from sequence 1
	Head    string // hash of the last record in Journal (the end canary)
	Count   int    // records in Journal, equal to EndSeq
	// StartSeq is the first start canary of the tag, EndSeq the last end canary.
	StartSeq, EndSeq uint64
}

// ExtractInterval cuts the journal bytes a running sink produced down to the interval of
// tag. A trailing partial line is a record the sink was still writing when the file was
// read; it is not yet acknowledged to anyone and is dropped. The chain must verify, and the
// tag must have a start canary before an end canary in it, or nothing is returned.
func ExtractInterval(raw []byte, tag string) (IntervalJournal, error) {
	if !tagRE.MatchString(tag) {
		return IntervalJournal{}, errors.New("netoracle: the interval tag is not 32 lowercase hex digits")
	}
	if i := bytes.LastIndexByte(raw, '\n'); i >= 0 {
		raw = raw[:i+1]
	} else {
		raw = nil
	}
	trace, err := audit.Verify(bytes.NewReader(raw))
	if err != nil {
		return IntervalJournal{}, fmt.Errorf("netoracle: the sink journal does not verify: %w", err)
	}
	var out IntervalJournal
	for _, rec := range trace.Records {
		if rec.Event.Kind != KindRequestStarted {
			continue
		}
		var rs RequestStarted
		if json.Unmarshal(rec.Event.Data, &rs) != nil || rs.Canary == nil || rs.Canary.Tag != tag {
			continue
		}
		switch rs.Canary.Phase {
		case PhaseStart:
			if out.StartSeq == 0 {
				out.StartSeq = rec.Sequence
			}
		case PhaseEnd:
			out.EndSeq = rec.Sequence
		}
	}
	if out.StartSeq == 0 || out.EndSeq <= out.StartSeq {
		return IntervalJournal{}, errors.New("netoracle: the journal has no start canary followed by an end canary for the interval")
	}
	// The bytes of records 1..EndSeq: one line each.
	end := 0
	for n := uint64(0); n < out.EndSeq; n++ {
		i := bytes.IndexByte(raw[end:], '\n')
		if i < 0 {
			return IntervalJournal{}, errors.New("netoracle: the journal lost a record while it was read")
		}
		end += i + 1
	}
	out.Journal = raw[:end]
	out.Count = int(out.EndSeq)
	out.Head = trace.Records[out.EndSeq-1].Hash
	return out, nil
}
