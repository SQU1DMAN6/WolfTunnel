package protocol

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	want := Message{Type: TypeRequest, Payload: []byte(`{"target":"https://example.com"}`)}
	var b bytes.Buffer
	if err := Write(&b, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(&b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != want.Type || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("round trip mismatch: %#v", got)
	}
}

func TestRejectWrongMagic(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, Message{Type: TypePing}); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	data[15] ^= 0xff
	if _, err := Read(bytes.NewReader(data)); err == nil {
		t.Fatal("expected invalid header error")
	}
}

func TestRejectOversizePayload(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, Message{Type: TypePing, Payload: make([]byte, MaxPayload+1)}); err == nil {
		t.Fatal("expected oversize write to fail")
	}
}
