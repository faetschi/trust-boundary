package webchat

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The supervisor generates one JSON fixture per protocol message
// (supervisor/internal/productprotocol/fixtures_test.go). Each must decode strictly
// into this module's copy of the wire types and encode back to the same bytes.

const protocolFixtures = "../../../../supervisor/internal/productprotocol/testdata"

func fixtureNames(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(protocolFixtures, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no protocol fixtures under %s: %v", protocolFixtures, err)
	}
	slices.Sort(files)
	return files
}

func TestProtocolFixturesRoundTripThroughTheWireCopy(t *testing.T) {
	var decoded []any
	requestOps := map[string]bool{}
	for _, path := range fixtureNames(t) {
		name := filepath.Base(path)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var message any
		switch {
		case strings.HasPrefix(name, "request-"):
			var request ProductRequest
			if err := strictDecode(raw, &request); err != nil {
				t.Errorf("%s does not decode strictly: %v", name, err)
				continue
			}
			if request.Version != ProductVersion {
				t.Errorf("%s version %q", name, request.Version)
			}
			requestOps[request.Op] = true
			message = request
		case strings.HasPrefix(name, "response-"):
			var response ProductResponse
			if err := strictDecode(raw, &response); err != nil {
				t.Errorf("%s does not decode strictly: %v", name, err)
				continue
			}
			if response.Version != ProductVersion || (response.Status != StatusOK && response.Status != StatusError) {
				t.Errorf("%s version %q status %q", name, response.Version, response.Status)
			}
			message = response
		default:
			t.Errorf("%s is neither a request nor a response fixture", name)
			continue
		}
		encoded, err := json.MarshalIndent(message, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(append(encoded, '\n'), raw) {
			t.Errorf("%s does not re-encode to the same bytes: the wire copy has drifted from the supervisor", name)
		}
		decoded = append(decoded, message)
	}
	for _, op := range []string{OpCreateSession, OpSubmitTurn, OpGetSession, OpListSessions, OpObserve, OpGetCandidate, OpCancelTurn, OpResolveTurn} {
		if !requestOps[op] {
			t.Errorf("no request fixture for operation %s", op)
		}
	}
	// A wire field that no fixture sets could drift unseen.
	set, want := map[string]bool{}, map[string]bool{}
	for _, message := range decoded {
		markSetFields(reflect.ValueOf(message), set)
	}
	collectFields(reflect.TypeOf(ProductRequest{}), want)
	collectFields(reflect.TypeOf(ProductResponse{}), want)
	for field := range want {
		if !set[field] {
			t.Errorf("no protocol fixture sets %s", field)
		}
	}
}

func markSetFields(v reflect.Value, set map[string]bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			markSetFields(v.Elem(), set)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			markSetFields(v.Index(i), set)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !v.Field(i).IsZero() {
				set[v.Type().Name()+"."+v.Type().Field(i).Name] = true
			}
			markSetFields(v.Field(i), set)
		}
	}
}

func collectFields(t reflect.Type, want map[string]bool) {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice:
		collectFields(t.Elem(), want)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			want[t.Name()+"."+t.Field(i).Name] = true
			collectFields(t.Field(i).Type, want)
		}
	}
}

func TestStrictDecodeRefusesWhatTheServiceFramingRefuses(t *testing.T) {
	deep := strings.Repeat("[", maxJSONDepth+2) + strings.Repeat("]", maxJSONDepth+2)
	cases := map[string]string{
		"duplicate key":          `{"version":"a","version":"b"}`,
		"nested duplicate key":   `{"session_id":"x","extra":{"a":1,"a":2}}`,
		"unknown field":          `{"version":"` + ProductVersion + `","admin":true}`,
		"trailing value":         `{"version":"x"}{}`,
		"trailing garbage":       `{"version":"x"} x`,
		"not json":               `nope`,
		"invalid utf-8":          "{\"version\":\"\xff\"}",
		"too deep":               `{"version":` + deep + `}`,
		"wrong type":             `{"version":7}`,
		"truncated":              `{"version":"x"`,
		"array where object":     `[]`,
		"empty":                  ``,
		"unescaped control char": "{\"version\":\"a\nb\"}",
	}
	for name, body := range cases {
		var response ProductResponse
		if err := strictDecode([]byte(body), &response); err == nil {
			t.Errorf("%s: strictDecode accepted %q", name, body)
		}
	}
	var good ProductResponse
	if err := strictDecode([]byte(`{"version":"`+ProductVersion+`","status":"ok"}`), &good); err != nil {
		t.Errorf("a valid response was refused: %v", err)
	}
}

func rawFrame(body string) *bytes.Buffer {
	var buffer bytes.Buffer
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
	buffer.Write(prefix[:])
	buffer.WriteString(body)
	return &buffer
}

func TestFramingRoundTripsAndBoundsSizes(t *testing.T) {
	var buffer bytes.Buffer
	in := ProductRequest{Version: ProductVersion, Op: OpObserve, Observe: &ProductObserve{SessionID: "psess-x", Cursor: 3, WaitMillis: 10}}
	if err := writeProductFrame(&buffer, in, MaxProductRequestBytes); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(buffer.Bytes(), []byte{0, 0, 0, byte(buffer.Len() - 4)}) {
		t.Errorf("the frame does not start with its 4-byte big-endian length")
	}
	var out ProductRequest
	if err := readProductFrame(&buffer, MaxProductRequestBytes, &out); err != nil || out.Observe == nil || *out.Observe != *in.Observe {
		t.Fatalf("round trip: %+v %v", out, err)
	}
	var response ProductResponse
	for name, frame := range map[string]*bytes.Buffer{
		"zero length":      rawFrame(""),
		"over the limit":   rawFrame(strings.Repeat("x", 40)),
		"duplicate key":    rawFrame(`{"version":"x","version":"y"}`),
		"unknown field":    rawFrame(`{"version":"x","admin":true}`),
		"trailing value":   rawFrame(`{"version":"x"}{}`),
		"length too large": bytes.NewBuffer([]byte{0xff, 0xff, 0xff, 0xff}),
	} {
		if err := readProductFrame(frame, 32, &response); !errors.Is(err, errProductFrame) {
			t.Errorf("%s: err=%v, want errProductFrame", name, err)
		}
	}
	if err := writeProductFrame(&bytes.Buffer{}, ProductResponse{Version: ProductVersion, Events: make([]ProductEvent, 10)}, 16); !errors.Is(err, errProductFrame) {
		t.Errorf("oversized write: err=%v", err)
	}
	// A frame cut short is an I/O error, not protocol: the caller sees a transport failure.
	if err := readProductFrame(bytes.NewBuffer([]byte{0, 0, 0, 9, '{'}), 32, &response); errors.Is(err, errProductFrame) || err == nil {
		t.Errorf("short frame: err=%v, want an I/O error", err)
	}
}
