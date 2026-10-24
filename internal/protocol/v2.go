package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

const Version = 2
const IdentifierV2 = "WolfTunnelv2"
const MaxPayloadV2 = 64 << 10
const (
	Hello byte = 16 + iota
	NodeIdentity
	EnrolmentRequest
	EnrolmentResult
	Authenticate
	AuthResult
	HTTPFetch
	HTTPResult
	PingV2
	PongV2
	Cancel
	Error
	Renew
	RenewResult
)

var ErrVersion = errors.New("UNSUPPORTED_VERSION")
var ErrLegacyVersion = fmt.Errorf("%w: legacy v0", ErrVersion)
var ErrPayload = errors.New("PROTOCOL_ERROR")
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func ValidID(s string) bool { return idPattern.MatchString(s) }

// Envelope is the reference JSON schema. No body, cookie or user header fields
// exist: the node returns metadata, never active web content.
type Envelope struct {
	RequestID    string            `json:"request_id"`
	Versions     []int             `json:"versions,omitempty"`
	Version      int               `json:"version,omitempty"`
	Fingerprint  string            `json:"fingerprint,omitempty"`
	Capabilities []string          `json:"capabilities,omitempty"`
	PublicKey    []byte            `json:"public_key,omitempty"`
	Code         string            `json:"code,omitempty"`
	ClientID     string            `json:"client_id,omitempty"`
	Signature    []byte            `json:"signature,omitempty"`
	Expires      time.Time         `json:"expires,omitzero"`
	Method       string            `json:"method,omitempty"`
	Target       string            `json:"target,omitempty"`
	Message      string            `json:"message,omitempty"`
	Status       int               `json:"status,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	FinalURL     string            `json:"final_url,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	ContentType  string            `json:"content_type,omitempty"`
	BodyLength   int64             `json:"body_length,omitempty"`
	DurationMS   int64             `json:"duration_ms,omitempty"`
	Redirects    int               `json:"redirects,omitempty"`
}

func ReadV2(r io.Reader) (Message, error) {
	var h [HeaderSize]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Message{}, err
	}
	if binary.BigEndian.Uint32(h[12:16]) != Magic {
		return Message{}, ErrInvalidHeader
	}
	if string(h[:12]) != IdentifierV2 {
		if string(h[:12]) == Identifier {
			n := binary.BigEndian.Uint32(h[17:])
			if n > MaxPayload {
				return Message{}, ErrPayload
			}
			if _, err := io.CopyN(io.Discard, r, int64(n)); err != nil {
				return Message{}, err
			}
			return Message{Type: h[16]}, ErrLegacyVersion
		}
		return Message{}, ErrVersion
	}
	n := binary.BigEndian.Uint32(h[17:])
	if n > MaxPayloadV2 || h[16] < Hello || h[16] > RenewResult {
		return Message{}, ErrPayload
	}
	p := make([]byte, int(n))
	_, err := io.ReadFull(r, p)
	return Message{Type: h[16], Payload: p}, err
}

func WriteV2(w io.Writer, typ byte, e Envelope) error {
	if typ < Hello || typ > RenewResult {
		return ErrPayload
	}
	p, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(p) > MaxPayloadV2 {
		return ErrPayload
	}
	var h [HeaderSize]byte
	copy(h[:12], IdentifierV2)
	binary.BigEndian.PutUint32(h[12:16], Magic)
	h[16] = typ
	binary.BigEndian.PutUint32(h[17:], uint32(len(p)))
	if err = writeFull(w, h[:]); err != nil {
		return err
	}
	return writeFull(w, p)
}

func Decode(m Message) (Envelope, error) {
	var e Envelope
	d := json.NewDecoder(bytes.NewReader(m.Payload))
	d.DisallowUnknownFields()
	if d.Decode(&e) != nil || d.Decode(new(any)) != io.EOF || !ValidID(e.RequestID) {
		return Envelope{}, ErrPayload
	}
	// Enforce per-message fields as well as the shared schema.
	fields := map[byte]string{
		Hello: "versions", NodeIdentity: "version fingerprint capabilities",
		EnrolmentRequest: "public_key code", EnrolmentResult: "client_id expires",
		Authenticate: "client_id signature", AuthResult: "client_id expires",
		HTTPFetch: "client_id signature method target", HTTPResult: "version fingerprint code message status reason final_url headers content_type body_length duration_ms redirects",
		PingV2: "client_id signature", PongV2: "version fingerprint", Cancel: "client_id signature",
		Error: "code message", Renew: "client_id signature", RenewResult: "client_id expires",
	}
	allowed, ok := fields[m.Type]
	if !ok {
		return Envelope{}, ErrPayload
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(m.Payload, &raw) != nil {
		return Envelope{}, ErrPayload
	}
	for k := range raw {
		if k != "request_id" && !bytes.Contains([]byte(" "+allowed+" "), []byte(" "+k+" ")) {
			return Envelope{}, ErrPayload
		}
	}
	return e, nil
}

func SigningBytes(binding []byte, typ byte, e Envelope) []byte {
	e.Signature = nil
	p, _ := json.Marshal(e)
	out := make([]byte, 0, len(binding)+1+len(p))
	out = append(out, binding...)
	out = append(out, typ)
	return append(out, p...)
}
