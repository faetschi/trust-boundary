package productprotocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestRequestNeedsExactlyItsPayload(t *testing.T) {
	good := Request{Version: Version, Op: OpGetSession, GetSession: &SessionRef{SessionID: "psess-x"}}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid request refused: %v", err)
	}
	if err := (Request{Version: Version, Op: OpListSessions}).Validate(); err != nil {
		t.Fatalf("list_sessions refused: %v", err)
	}
	for name, r := range map[string]Request{
		"wrong version":   {Version: "v0", Op: OpGetSession, GetSession: &SessionRef{}},
		"unknown op":      {Version: Version, Op: "drop_tables"},
		"missing payload": {Version: Version, Op: OpSubmitTurn},
		"other payload":   {Version: Version, Op: OpSubmitTurn, GetSession: &SessionRef{}},
		"two payloads":    {Version: Version, Op: OpGetSession, GetSession: &SessionRef{}, Observe: &Observe{}},
		"list with extra": {Version: Version, Op: OpListSessions, GetSession: &SessionRef{}},
		"wait too long":   {Version: Version, Op: OpObserve, Observe: &Observe{WaitMillis: MaxObserveWaitMillis + 1}},
		"negative wait":   {Version: Version, Op: OpObserve, Observe: &Observe{WaitMillis: -1}},
	} {
		if err := r.Validate(); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: err=%v, want ErrProtocol", name, err)
		}
	}
}

func TestFramesRoundTripAndRefuseMalformedInput(t *testing.T) {
	var buf bytes.Buffer
	in := Request{Version: Version, Op: OpObserve, Observe: &Observe{SessionID: "psess-x", Cursor: 3, WaitMillis: 10}}
	if err := WriteFrame(&buf, in, MaxRequestBytes); err != nil {
		t.Fatal(err)
	}
	var out Request
	if err := ReadFrame(&buf, MaxRequestBytes, &out); err != nil || out.Observe == nil || *out.Observe != *in.Observe {
		t.Fatalf("round trip: %+v %v", out, err)
	}
	frame := func(body string) *bytes.Buffer {
		var b bytes.Buffer
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
		b.Write(prefix[:])
		b.WriteString(body)
		return &b
	}
	for name, body := range map[string]string{
		"unknown field":  `{"version":"` + Version + `","op":"list_sessions","admin":true}`,
		"duplicate key":  `{"version":"x","version":"y","op":"list_sessions"}`,
		"trailing value": `{"version":"x","op":"list_sessions"}{}`,
		"not json":       `nope`,
	} {
		var r Request
		if err := ReadFrame(frame(body), MaxRequestBytes, &r); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: err=%v, want ErrProtocol", name, err)
		}
	}
	var r Request
	big := frame(string(make([]byte, 16)))
	if err := ReadFrame(big, 8, &r); !errors.Is(err, ErrProtocol) {
		t.Errorf("oversized frame: err=%v", err)
	}
	if err := WriteFrame(&bytes.Buffer{}, Response{Version: Version, Status: "ok", Events: make([]Event, 10)}, 16); !errors.Is(err, ErrProtocol) {
		t.Errorf("oversized write: err=%v", err)
	}
}
