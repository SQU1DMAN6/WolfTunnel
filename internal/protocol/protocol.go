package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	Identifier = "WolfTunnelv0" // exactly 12 bytes
	Magic      = uint32(0xF00DBEEF)
	HeaderSize = 12 + 4 + 1 + 4
	MaxPayload = 1 << 20 // 1 MiB hard limit

	TypeRequest byte = 1
	TypeAck     byte = 2
	TypePing    byte = 3
	TypePong    byte = 4
)

var ErrInvalidHeader = errors.New("invalid WolfTunnel protocol header")

type Message struct {
	Type    byte
	Payload []byte
}

func Write(w io.Writer, m Message) error {
	if len(m.Payload) > MaxPayload {
		return fmt.Errorf("payload exceeds %d bytes", MaxPayload)
	}
	var h [HeaderSize]byte
	copy(h[0:12], []byte(Identifier))
	binary.BigEndian.PutUint32(h[12:16], Magic)
	h[16] = m.Type
	binary.BigEndian.PutUint32(h[17:21], uint32(len(m.Payload)))
	if err := writeFull(w, h[:]); err != nil {
		return err
	}
	if len(m.Payload) > 0 {
		return writeFull(w, m.Payload)
	}
	return nil
}

func Read(r io.Reader) (Message, error) {
	var h [HeaderSize]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Message{}, err
	}
	if string(h[0:12]) != Identifier || binary.BigEndian.Uint32(h[12:16]) != Magic {
		return Message{}, ErrInvalidHeader
	}
	n := binary.BigEndian.Uint32(h[17:21])
	if n > MaxPayload {
		return Message{}, fmt.Errorf("payload length %d exceeds limit", n)
	}
	p := make([]byte, int(n))
	if _, err := io.ReadFull(r, p); err != nil {
		return Message{}, err
	}
	return Message{Type: h[16], Payload: p}, nil
}

type Request struct {
	RequestID string `json:"request_id"`
	Token     string `json:"token"`
	Method    string `json:"method"`
	Target    string `json:"target,omitempty"`
}

type Response struct {
	RequestID  string `json:"request_id,omitempty"`
	Status     string `json:"status"`
	State      string `json:"state"`
	CanRespond bool   `json:"can_respond"`
}

type Ping struct {
	Token string `json:"token"`
}

func writeFull(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
