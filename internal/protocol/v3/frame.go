// Package v3 defines the remote-browser protocol independently of v0/v2.
package v3

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
	"wolftunnel/internal/browserdiag"
)

const Identifier = "WolfTunnelv3"
const HeaderSize = 48
const MaxControl = 64 << 10
const MaxPixel = 8 << 20
const FullFrame uint16 = 1
const (
	Hello byte = 1 + iota
	NodeIdentity
	Authenticate
	AuthResult
	SessionCreate
	SessionCreated
	PixelAttach
	SessionClose
	SessionStatus
	Navigate
	NavigationStatus
	InputEvent
	InputAck
	ViewportSet
	ViewportAck
	PixelUpdate
	FrameRefresh
	Cancel
	Ping
	Pong
	Error
	TabCreate
	TabSelect
	TabClose
	History
)

var ErrFrame = errors.New("PROTOCOL_ERROR")
var ErrVersion = errors.New("UNSUPPORTED_VERSION")

type ID [16]byte

func NewID() ID {
	var id ID
	if _, e := rand.Read(id[:]); e != nil {
		panic("secure randomness unavailable")
	}
	return id
}
func ParseID(s string) (ID, error) {
	var id ID
	b, e := hex.DecodeString(s)
	if e != nil || len(b) != 16 {
		return id, ErrFrame
	}
	copy(id[:], b)
	return id, nil
}
func (id ID) String() string { return hex.EncodeToString(id[:]) }
func (id ID) Zero() bool     { return id == ID{} }

type Frame struct {
	Type     byte
	Flags    uint16
	Session  ID
	Sequence uint64
	Payload  []byte
}

func Read(r io.Reader, pixels bool) (Frame, error) {
	var h [HeaderSize]byte
	if _, e := io.ReadFull(r, h[:]); e != nil {
		return Frame{}, e
	}
	if string(h[:12]) != Identifier || h[12] != 3 {
		return Frame{}, ErrVersion
	}
	typ := h[13]
	flags := binary.BigEndian.Uint16(h[14:16])
	n := binary.BigEndian.Uint32(h[16:20])
	limit := uint32(MaxControl)
	if binary.BigEndian.Uint32(h[44:]) != 0xF00DBEEF || typ < Hello || typ > History || (typ == PixelUpdate && (!pixels || flags != FullFrame)) || (typ != PixelUpdate && flags != 0) {
		return Frame{}, ErrFrame
	}
	if typ == PixelUpdate {
		limit = MaxPixel
	}
	if n > limit || binary.BigEndian.Uint64(h[36:44]) == 0 {
		return Frame{}, ErrFrame
	}
	f := Frame{Type: typ, Flags: flags, Sequence: binary.BigEndian.Uint64(h[36:44]), Payload: make([]byte, n)}
	copy(f.Session[:], h[20:36])
	_, e := io.ReadFull(r, f.Payload)
	return f, e
}
func Write(w io.Writer, f Frame) error {
	max := MaxControl
	if f.Type == PixelUpdate {
		max = MaxPixel
	}
	if len(f.Payload) > max || f.Type < Hello || f.Type > History || f.Sequence == 0 || (f.Type == PixelUpdate && f.Flags != FullFrame) || (f.Type != PixelUpdate && f.Flags != 0) {
		return ErrFrame
	}
	var h [HeaderSize]byte
	copy(h[:12], Identifier)
	h[12] = 3
	h[13] = f.Type
	binary.BigEndian.PutUint16(h[14:16], f.Flags)
	binary.BigEndian.PutUint32(h[16:20], uint32(len(f.Payload)))
	copy(h[20:36], f.Session[:])
	binary.BigEndian.PutUint64(h[36:44], f.Sequence)
	binary.BigEndian.PutUint32(h[44:], 0xF00DBEEF)
	if e := writeAll(w, h[:]); e != nil {
		return e
	}
	return writeAll(w, f.Payload)
}
func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, e := w.Write(p)
		if e != nil {
			return e
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

type Input struct {
	Kind      string  `json:"kind"`
	X         float64 `json:"x,omitempty"`
	Y         float64 `json:"y,omitempty"`
	Button    int     `json:"button,omitempty"`
	DeltaX    float64 `json:"delta_x,omitempty"`
	DeltaY    float64 `json:"delta_y,omitempty"`
	Code      string  `json:"code,omitempty"`
	Key       string  `json:"key,omitempty"`
	Text      string  `json:"text,omitempty"`
	Modifiers int     `json:"modifiers,omitempty"`
}

var keyCode = regexp.MustCompile(`^(Key[A-Z]|Digit[0-9]|F([1-9]|1[0-2])|Arrow(Left|Right|Up|Down)|Enter|Tab|Backspace|Delete|Escape|Space|Home|End|PageUp|PageDown|Shift(Left|Right)|Control(Left|Right)|Alt(Left|Right)|Meta(Left|Right)|Minus|Equal|BracketLeft|BracketRight|Backslash|Semicolon|Quote|Comma|Period|Slash|Backquote|CapsLock|Insert)$`)

func (i Input) Validate(w, h int) error {
	for _, x := range []float64{i.X, i.Y, i.DeltaX, i.DeltaY} {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return ErrFrame
		}
	}
	if i.Modifiers < 0 || i.Modifiers > 15 || !utf8.ValidString(i.Text) || len(i.Text) > 4096 || len(i.Key) > 64 || strings.ContainsRune(i.Key, 0) {
		return ErrFrame
	}
	switch i.Kind {
	case "move", "down", "up", "wheel":
		if i.X < 0 || i.Y < 0 || i.X >= float64(w) || i.Y >= float64(h) {
			return ErrFrame
		}
		if (i.Kind == "down" || i.Kind == "up") && (i.Button < 1 || i.Button > 3) {
			return ErrFrame
		}
		if math.Abs(i.DeltaX) > 4096 || math.Abs(i.DeltaY) > 4096 {
			return ErrFrame
		}
	case "key_down", "key_up":
		if !keyCode.MatchString(i.Code) {
			return ErrFrame
		}
	case "text":
		if i.Text == "" {
			return ErrFrame
		}
	case "blur":
	default:
		return ErrFrame
	}
	return nil
}

type Control struct {
	BrowserUS    int64    `json:"browser_us,omitempty"`
	Versions     []int    `json:"versions,omitempty"`
	Version      int      `json:"version,omitempty"`
	Fingerprint  string   `json:"fingerprint,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	ClientID     string   `json:"client_id,omitempty"`
	Role         string   `json:"role,omitempty"`
	Signature    []byte   `json:"signature,omitempty"`
	Tab          string   `json:"tab,omitempty"`
	Target       string   `json:"target,omitempty"`
	Width        int      `json:"width,omitempty"`
	Height       int      `json:"height,omitempty"`
	MaxTabs      int      `json:"max_tabs,omitempty"`
	Quality      int      `json:"quality,omitempty"`
	FPS          int      `json:"fps,omitempty"`
	State        string   `json:"state,omitempty"`
	Code         string   `json:"code,omitempty"`
	Status       int      `json:"status,omitempty"`
	Title        string   `json:"title,omitempty"`
	Input        *Input   `json:"input,omitempty"`
	Direction    int      `json:"direction,omitempty"`
	DispatchUS   int64    `json:"dispatch_us,omitempty"`
	Ack          uint64   `json:"ack,omitempty"`
}

var fields = map[byte]string{Hello: "versions", NodeIdentity: "version fingerprint capabilities", Authenticate: "client_id role signature capabilities", AuthResult: "client_id", SessionCreate: "width height quality fps", SessionCreated: "width height max_tabs", PixelAttach: "", SessionClose: "", SessionStatus: "tab state code", Navigate: "tab target", NavigationStatus: "tab state code status title target", InputEvent: "tab input", InputAck: "ack dispatch_us browser_us", ViewportSet: "width height", ViewportAck: "width height", FrameRefresh: "", Cancel: "tab", Ping: "", Pong: "ack", Error: "tab code", TabCreate: "tab", TabSelect: "tab", TabClose: "tab", History: "tab direction"}

func Encode(c Control) []byte { b, _ := json.Marshal(c); return b }
func Decode(f Frame) (Control, error) {
	var c Control
	if len(f.Payload) > MaxControl {
		return c, ErrFrame
	}
	d := json.NewDecoder(bytes.NewReader(f.Payload))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return Control{}, ErrFrame
	}
	allowed, ok := fields[f.Type]
	if !ok {
		return c, ErrFrame
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(f.Payload, &raw) != nil || raw == nil {
		return c, ErrFrame
	}
	for k := range raw {
		if !strings.Contains(" "+allowed+" ", " "+k+" ") {
			return Control{}, ErrFrame
		}
	}
	if c.Tab != "" {
		if _, e := ParseID(c.Tab); e != nil {
			return Control{}, ErrFrame
		}
	}
	if len(c.Target) > 8192 || len(c.Title) > 256 {
		return Control{}, ErrFrame
	}
	if c.Input != nil {
		if e := c.Input.Validate(3840, 2160); e != nil {
			return Control{}, e
		}
	}
	// A known field name does not make an empty or ill-typed state transition
	// valid. Session handlers additionally check ownership, dimensions and state.
	switch f.Type {
	case Hello:
		if len(c.Versions) == 0 || len(c.Versions) > 8 {
			return Control{}, ErrFrame
		}
	case NodeIdentity:
		if c.Version != 3 || len(c.Fingerprint) != 64 || len(c.Capabilities) > 16 {
			return Control{}, ErrFrame
		}
	case Authenticate:
		if len(c.ClientID) != 64 || len(c.Signature) != ed25519.SignatureSize || (c.Role != "control" && c.Role != "pixel") {
			return Control{}, ErrFrame
		}
		if len(c.Capabilities) > 1 || (len(c.Capabilities) == 1 && c.Capabilities[0] != "browser-start-diagnostics") {
			return Control{}, ErrFrame
		}
	case AuthResult:
		if len(c.ClientID) != 64 {
			return Control{}, ErrFrame
		}
	case SessionCreated:
		if c.Width < 320 || c.Width > 3840 || c.Height < 200 || c.Height > 2160 || c.MaxTabs < 1 || c.MaxTabs > 8 {
			return Control{}, ErrFrame
		}
	case Navigate, Cancel, TabCreate, TabSelect, TabClose, History, InputEvent:
		id, err := ParseID(c.Tab)
		if err != nil || id.Zero() || (f.Type == Navigate && c.Target == "") || (f.Type == InputEvent && c.Input == nil) || (f.Type == History && c.Direction != -1 && c.Direction != 1) {
			return Control{}, ErrFrame
		}
	case InputAck, Pong:
		if c.Ack == 0 || c.DispatchUS < 0 || c.BrowserUS < 0 {
			return Control{}, ErrFrame
		}
	case Error:
		if c.Code == "" || SafeCode(c.Code) != c.Code {
			return Control{}, ErrFrame
		}
	}
	return c, nil
}
func Proof(binding []byte, session ID, c Control) []byte {
	c.Signature = nil
	p := append([]byte(nil), binding...)
	p = append(p, Authenticate)
	p = append(p, session[:]...)
	return append(p, Encode(c)...)
}
func Sign(key ed25519.PrivateKey, binding []byte, id ID, c Control) Control {
	c.Signature = ed25519.Sign(key, Proof(binding, id, c))
	return c
}
func SafeCode(code string) string {
	if _, ok := browserdiag.Lookup(code); ok {
		return code
	}
	switch code {
	case "", "PROTOCOL_ERROR", "UNSUPPORTED_VERSION", "AUTH_FAILED", "SESSION_INVALID", "SESSION_LIMIT", "TAB_LIMIT", "BROWSER_UNAVAILABLE", "BROWSER_START_FAILED", "BROWSER_TIMEOUT", "BROWSER_CRASHED", "NAVIGATION_FAILED", "NODE_POLICY_REJECTED", "NODE_DNS_ERROR", "NODE_UNREACHABLE", "VIEWPORT_REJECTED", "PIXEL_ERROR", "RESOURCE_LIMIT", "CANCELLED", "INTERNAL_ERROR":
		return code
	default:
		return "INTERNAL_ERROR"
	}
}
