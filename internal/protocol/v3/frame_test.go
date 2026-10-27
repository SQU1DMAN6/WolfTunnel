package v3

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"testing"
)

func TestFramesBoundsAndSchemas(t *testing.T) {
	var buf bytes.Buffer
	id := NewID()
	if e := Write(&buf, Frame{Type: Navigate, Session: id, Sequence: 1, Payload: Encode(Control{Tab: NewID().String(), Target: "https://example.com"})}); e != nil {
		t.Fatal(e)
	}
	raw := append([]byte(nil), buf.Bytes()...)
	f, e := Read(&buf, false)
	if e != nil || f.Session != id {
		t.Fatal(e)
	}
	if _, e = Decode(f); e != nil {
		t.Fatal(e)
	}
	mutations := []func([]byte) []byte{func(p []byte) []byte { return p[:47] }, func(p []byte) []byte { p[12] = 2; return p }, func(p []byte) []byte { p[13] = 255; return p }, func(p []byte) []byte { p[14] = 1; return p }, func(p []byte) []byte { binary.BigEndian.PutUint32(p[16:20], ^uint32(0)); return p }, func(p []byte) []byte { binary.BigEndian.PutUint64(p[36:44], 0); return p }, func(p []byte) []byte { p[47] ^= 1; return p }, func(p []byte) []byte { return p[:len(p)-1] }}
	for i, change := range mutations {
		if _, e := Read(bytes.NewReader(change(append([]byte(nil), raw...))), false); e == nil {
			t.Fatalf("accepted mutation %d", i)
		}
	}
	for _, p := range []string{`{}`, `null`, `{"target":"https://example.com","method":"Runtime.evaluate"}`, `{"target":"x"} {}`, `{"tab":"bad"}`, `{"input":{"kind":"text","text":"x","unknown":1}}`} {
		typ := Navigate
		if bytes.Contains([]byte(p), []byte("input")) {
			typ = InputEvent
		}
		if p == `{}` {
			typ = PixelUpdate
		}
		if _, e := Decode(Frame{Type: typ, Payload: []byte(p)}); e == nil {
			t.Fatalf("accepted invalid control %s", p)
		}
	}
	buf.Reset()
	if e = Write(&buf, Frame{Type: PixelUpdate, Flags: FullFrame, Session: id, Sequence: 2, Payload: make([]byte, MaxControl+1)}); e != nil {
		t.Fatal(e)
	}
	if _, e = Read(&buf, false); e == nil {
		t.Fatal("pixel accepted on control channel")
	}
}
func TestChannelProofBindsSessionRoleAndConnection(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	binding := make([]byte, 32)
	id := NewID()
	c := Sign(key, binding, id, Control{ClientID: "public-id", Role: "pixel"})
	if !ed25519.Verify(pub, Proof(binding, id, c), c.Signature) {
		t.Fatal("valid proof rejected")
	}
	other := append([]byte(nil), binding...)
	other[0] = 1
	if ed25519.Verify(pub, Proof(other, id, c), c.Signature) {
		t.Fatal("connection replay")
	}
	if ed25519.Verify(pub, Proof(binding, NewID(), c), c.Signature) {
		t.Fatal("session substitution")
	}
	mut := c
	mut.Role = "control"
	if ed25519.Verify(pub, Proof(binding, id, mut), c.Signature) {
		t.Fatal("role substitution")
	}
}
func FuzzV3Frame(f *testing.F) {
	var b bytes.Buffer
	_ = Write(&b, Frame{Type: Hello, Sequence: 1, Payload: Encode(Control{Versions: []int{3, 2}})})
	f.Add(b.Bytes())
	f.Add([]byte(Identifier))
	f.Fuzz(func(t *testing.T, p []byte) {
		frame, e := Read(bytes.NewReader(p), true)
		if e == nil {
			if frame.Type != PixelUpdate {
				_, _ = Decode(frame)
			}
			if len(frame.Payload) > MaxPixel {
				t.Fatal("unbounded frame")
			}
		}
	})
}
func FuzzV3Control(f *testing.F) {
	f.Add(byte(InputEvent), Encode(Control{Tab: NewID().String(), Input: &Input{Kind: "text", Text: "Привет"}}))
	f.Add(byte(Authenticate), Encode(Control{ClientID: "x", Role: "control"}))
	f.Fuzz(func(t *testing.T, typ byte, p []byte) {
		if len(p) > MaxControl {
			return
		}
		c, e := Decode(Frame{Type: typ, Payload: p})
		if e == nil && c.Input != nil {
			_ = c.Input.Validate(1920, 1080)
		}
	})
}

func TestInputValidation(t *testing.T) {
	for _, i := range []Input{{Kind: "text", Text: ""}, {Kind: "move", X: -1}, {Kind: "down", Button: 9}, {Kind: "wheel", DeltaY: 5000}, {Kind: "key_down", Code: "OS_EXEC"}, {Kind: "unknown"}, {Kind: "key_up", Code: "KeyA", Modifiers: 99}} {
		if i.Validate(1920, 1080) == nil {
			t.Fatalf("invalid input kind %s accepted", i.Kind)
		}
	}
	for _, i := range []Input{{Kind: "text", Text: "✓ Привет"}, {Kind: "blur"}, {Kind: "wheel", X: 10, Y: 20, DeltaY: 80}, {Kind: "key_down", Code: "ShiftLeft", Key: "Shift", Modifiers: 8}, {Kind: "key_up", Code: "ShiftLeft", Key: "Shift"}} {
		if e := i.Validate(1920, 1080); e != nil {
			t.Fatal(e)
		}
	}
}

func TestServerReplySchemas(t *testing.T) {
	// Persistent connections must survive heartbeats and tab-scoped failures.
	for typ, c := range map[byte]Control{Pong: {Ack: 42}, InputAck: {Ack: 12, DispatchUS: 20, BrowserUS: 5000}, Error: {Tab: NewID().String(), Code: "VIEWPORT_REJECTED"}} {
		if _, e := Decode(Frame{Type: typ, Payload: Encode(c)}); e != nil {
			t.Fatalf("reply type %d rejected: %v", typ, e)
		}
	}
}

func TestStartupCodesCrossProtocolButRawMessagesDoNot(t *testing.T) {
	for _, code := range []string{"BROWSER_NAMESPACE_DENIED", "BROWSER_USER_SESSION_UNAVAILABLE", "BROWSER_DEPENDENCY_MISSING", "BROWSER_MEMORY_LIMIT", "BROWSER_TASK_LIMIT", "BROWSER_SANDBOX_REJECTED"} {
		_, err := Decode(Frame{Type: Error, Payload: Encode(Control{Code: code})})
		if err != nil || SafeCode(code) != code {
			t.Fatal("startup diagnosis lost", code, err)
		}
	}
	if SafeCode("https://private.test/secret") != "INTERNAL_ERROR" {
		t.Fatal("raw error allowed")
	}
}

type fragmentWriter struct{ bytes.Buffer }

func (w *fragmentWriter) Write(p []byte) (int, error) { return w.Buffer.Write(p[:min(3, len(p))]) }

type fragmentReader struct{ *bytes.Reader }

func (r fragmentReader) Read(p []byte) (int, error) { return r.Reader.Read(p[:min(2, len(p))]) }
func TestPartialReadWriteAndGestureBounds(t *testing.T) {
	w := &fragmentWriter{}
	f := Frame{Type: InputEvent, Sequence: 1, Payload: Encode(Control{Tab: NewID().String(), Input: &Input{Kind: "text", Text: "✓ Привет 日本語"}})}
	if e := Write(w, f); e != nil {
		t.Fatal(e)
	}
	got, e := Read(fragmentReader{bytes.NewReader(w.Bytes())}, false)
	if e != nil || !bytes.Equal(got.Payload, f.Payload) {
		t.Fatal("fragmented stream", e)
	}
	for _, i := range []Input{{Kind: "pinch", Scale: 0}, {Kind: "pinch", Scale: 5}, {Kind: "move", Scale: 1}, {Kind: "key_up", Code: "KeyA", Repeat: true}, {Kind: "text", Text: string([]byte{255})}} {
		if i.Validate(900, 650) == nil {
			t.Fatal("invalid extension accepted", i.Kind)
		}
	}
	for _, i := range []Input{{Kind: "pinch", X: 80, Y: 90, Scale: 1.025}, {Kind: "wheel", X: 80, Y: 90, DeltaX: 0.25, DeltaY: -0.125}, {Kind: "key_down", Code: "KeyA", Key: "ä", Repeat: true}} {
		if e := i.Validate(900, 650); e != nil {
			t.Fatal(e)
		}
	}
}
