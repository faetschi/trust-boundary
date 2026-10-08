package providerbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"

	"tbound/supervisor/internal/broker/protocol"
)

const (
	channelPrefixBytes = 4
	maxChannelFrames   = 2048
)

// channelRequest is deliberately unable to carry prompt text, history, model,
// endpoint, headers, API keys, tool calls, or tool results.
type channelRequest struct {
	Version   string `json:"version"`
	Sequence  uint64 `json:"sequence"`
	Kind      string `json:"kind"`
	RequestID string `json:"request_id"`
}

type channelResponse struct {
	Version   string `json:"version"`
	Sequence  uint64 `json:"sequence"`
	Kind      string `json:"kind"`
	RequestID string `json:"request_id"`
	Turn      *Turn  `json:"turn,omitempty"`
	Error     string `json:"error,omitempty"`
}

type incomingFrame struct {
	request channelRequest
	err     error
}

type exchangeResult struct {
	requestID string
	turn      Turn
	err       error
}

// Serve runs the persistent worker side of the inherited duplex channel. It
// serializes provider turns, processes cancellation while HTTP is in flight,
// and closes the conversation on malformed, replayed, or overlapping input.
func Serve(ctx context.Context, channel io.ReadWriteCloser, conversation *Conversation) error {
	if ctx == nil || channel == nil || conversation == nil {
		return errors.New("provider bridge requires context, inherited channel, and conversation")
	}
	ctx, cancel := context.WithCancel(ctx)

	requests := make(chan incomingFrame, 1)
	results := make(chan exchangeResult, 1)
	go readRequests(ctx, channel, requests)

	var nextInbound uint64 = 1
	var nextOutbound uint64 = 1
	var requestCount uint64
	var inflight bool
	var activeID string
	var cancelActive context.CancelFunc
	defer func() {
		if cancelActive != nil {
			cancelActive()
		}
		cancel()
		_ = channel.Close()
		conversation.Close()
	}()
	context.AfterFunc(ctx, func() { _ = channel.Close() })
	seenRequestIDs := make(map[string]struct{})
	cancelledIDs := make(map[string]struct{})
	var writeMu sync.Mutex

	writeResponse := func(response channelResponse) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if nextOutbound == math.MaxUint64 {
			return errors.New("provider channel response sequence exhausted")
		}
		response.Version = bridgeSchemaVersion
		response.Sequence = nextOutbound
		encoded, err := json.Marshal(response)
		if err != nil {
			return fmt.Errorf("encode provider channel response: %w", err)
		}
		if len(encoded) == 0 || len(encoded) > protocol.MaxFrameBytes {
			return errors.New("provider channel response exceeds frame bound")
		}
		var prefix [channelPrefixBytes]byte
		binary.BigEndian.PutUint32(prefix[:], uint32(len(encoded)))
		if err := writeAll(channel, prefix[:]); err != nil {
			return fmt.Errorf("write provider channel frame prefix: %w", err)
		}
		if err := writeAll(channel, encoded); err != nil {
			return fmt.Errorf("write provider channel frame: %w", err)
		}
		nextOutbound++
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			if cancelActive != nil {
				cancelActive()
			}
			return nil
		case incoming := <-requests:
			if incoming.err != nil {
				if cancelActive != nil {
					cancelActive()
				}
				if !inflight && errors.Is(incoming.err, io.EOF) {
					return nil
				}
				return incoming.err
			}
			req := incoming.request
			if req.Sequence != nextInbound || req.Sequence == math.MaxUint64 {
				return errors.New("provider channel request is replayed or out of order")
			}
			nextInbound++
			requestCount++
			if requestCount > maxChannelFrames {
				return errors.New("provider channel frame budget exhausted")
			}
			switch req.Kind {
			case "next":
				if inflight || !validText(req.RequestID, MaxIdentityBytes) {
					return errors.New("provider next request overlaps or has invalid request ID")
				}
				if _, replayed := seenRequestIDs[req.RequestID]; replayed {
					return errors.New("provider channel request ID was replayed")
				}
				seenRequestIDs[req.RequestID] = struct{}{}
				inflight = true
				activeID = req.RequestID
				turnCtx, turnCancel := context.WithCancel(ctx)
				cancelActive = turnCancel
				go func(id string) {
					turn, err := conversation.NextTurn(turnCtx)
					results <- exchangeResult{requestID: id, turn: turn, err: err}
				}(activeID)
			case "cancel":
				if !inflight || req.RequestID != activeID || cancelActive == nil {
					return errors.New("provider cancel does not match the active request")
				}
				if _, replayed := cancelledIDs[req.RequestID]; replayed {
					return errors.New("provider cancellation was replayed")
				}
				cancelledIDs[req.RequestID] = struct{}{}
				cancelActive()
			default:
				return fmt.Errorf("unsupported provider channel request kind %q", req.Kind)
			}
		case result := <-results:
			if !inflight || result.requestID != activeID {
				return errors.New("provider exchange completion does not match active request")
			}
			if cancelActive != nil {
				cancelActive()
			}
			inflight = false
			activeID = ""
			cancelActive = nil
			if result.err != nil {
				if err := writeResponse(channelResponse{Kind: "error", RequestID: result.requestID, Error: safeFailure(result.err)}); err != nil {
					return err
				}
				return fmt.Errorf("provider turn failed; conversation closed: %w", result.err)
			}
			turn := result.turn
			if err := writeResponse(channelResponse{Kind: "turn", RequestID: result.requestID, Turn: &turn}); err != nil {
				return err
			}
		}
	}
}

func readRequests(ctx context.Context, channel io.ReadWriteCloser, output chan<- incomingFrame) {
	var sequence uint64 = 1
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		request, err := readChannelRequest(channel)
		if err != nil {
			select {
			case output <- incomingFrame{err: err}:
			case <-ctx.Done():
			}
			return
		}
		if request.Sequence != sequence || sequence == math.MaxUint64 {
			select {
			case output <- incomingFrame{err: errors.New("provider channel request is replayed or out of order")}:
			case <-ctx.Done():
			}
			return
		}
		sequence++
		select {
		case output <- incomingFrame{request: request}:
		case <-ctx.Done():
			return
		}
	}
}

func readChannelRequest(reader io.Reader) (channelRequest, error) {
	var request channelRequest
	var prefix [channelPrefixBytes]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return request, fmt.Errorf("read provider frame prefix: %w", err)
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length == 0 || length > protocol.MaxFrameBytes {
		return request, errors.New("provider frame length is invalid")
	}
	encoded := make([]byte, int(length))
	if _, err := io.ReadFull(reader, encoded); err != nil {
		return request, fmt.Errorf("read provider frame: %w", err)
	}
	if err := protocol.ValidateStrictJSON(encoded); err != nil {
		return request, fmt.Errorf("validate provider frame: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return request, err
	}
	if len(fields) != 4 {
		return request, errors.New("provider request must contain exactly four fields")
	}
	for _, name := range []string{"version", "sequence", "kind", "request_id"} {
		if _, ok := fields[name]; !ok {
			return request, fmt.Errorf("provider request is missing exact field %q", name)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	if request.Version != bridgeSchemaVersion || request.Sequence == 0 ||
		(request.Kind != "next" && request.Kind != "cancel") || !validText(request.RequestID, MaxIdentityBytes) {
		return request, errors.New("provider request field value is invalid")
	}
	return request, nil
}

func writeAll(writer io.Writer, bytes []byte) error {
	for len(bytes) != 0 {
		n, err := writer.Write(bytes)
		if n < 0 || n > len(bytes) {
			return errors.New("provider channel writer returned invalid byte count")
		}
		bytes = bytes[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
