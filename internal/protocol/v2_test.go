package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestV2FramesAndValidation(t *testing.T) {
	var b bytes.Buffer
	want := Envelope{RequestID: "hello-1", Versions: []int{2}}
	if err := WriteV2(&b, Hello, want); err != nil {
		t.Fatal(err)
	}
	valid := append([]byte(nil), b.Bytes()...)
	m, err := ReadV2(&b)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(m)
	if err != nil || got.RequestID != want.RequestID || got.Versions[0] != 2 {
		t.Fatalf("decode: %+v %v", got, err)
	}
	for _, tc := range []struct {
		name   string
		change func([]byte) []byte
	}{
		{"magic", func(b []byte) []byte { b[12] ^= 1; return b }},
		{"version", func(b []byte) []byte { copy(b[:12], Identifier); return b }},
		{"length", func(b []byte) []byte { binary.BigEndian.PutUint32(b[17:], MaxPayloadV2+1); return b }},
		{"type", func(b []byte) []byte { b[16] = 255; return b }},
		{"truncated", func(b []byte) []byte { return b[:len(b)-1] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadV2(bytes.NewReader(tc.change(append([]byte(nil), valid...)))); err == nil {
				t.Fatal("accepted malformed frame")
			}
		})
	}
	b.Reset()
	if err := Write(&b, Message{Type: TypePing}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadV2(&b); !errors.Is(err, ErrVersion) {
		t.Fatal(err)
	}
	for _, payload := range []string{`{}`, `{"request_id":"bad space"}`, `{"request_id":"x","token":"secret"}`, `{"request_id":"x","target":"https://example.com"}`, `{"request_id":"x"} {}`, `null`} {
		if _, err := Decode(Message{Type: Hello, Payload: []byte(payload)}); err == nil {
			t.Fatalf("accepted %s", payload)
		}
	}
	if err := WriteV2(io.Discard, HTTPFetch, Envelope{RequestID: "x", Target: string(make([]byte, MaxPayloadV2+1))}); err == nil {
		t.Fatal("accepted oversized write")
	}
}
func FuzzFrameV2(f *testing.F) {
	var b bytes.Buffer
	_ = WriteV2(&b, Hello, Envelope{RequestID: "seed", Versions: []int{2}})
	f.Add(b.Bytes())
	f.Add([]byte("WolfTunnelv0"))
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := ReadV2(bytes.NewReader(b))
		if err == nil {
			if len(m.Payload) > MaxPayloadV2 {
				t.Fatal("unbounded frame")
			}
			_, _ = Decode(m)
		}
	})
}
func FuzzAuthPayload(f *testing.F) {
	f.Add(byte(Authenticate), []byte(`{"request_id":"auth","client_id":"x","signature":"AA=="}`))
	f.Add(byte(EnrolmentRequest), []byte(`{"request_id":"enrol","code":"x","public_key":"AA=="}`))
	f.Fuzz(func(t *testing.T, typ byte, p []byte) {
		if len(p) > MaxPayloadV2 {
			return
		}
		e, err := Decode(Message{Type: typ, Payload: p})
		if err == nil && !ValidID(e.RequestID) {
			t.Fatal("invalid request ID")
		}
	})
}
