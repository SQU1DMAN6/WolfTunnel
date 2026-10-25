package session

import (
	"bytes"
	"net"
	"testing"
	"time"
	v3 "wolftunnel/internal/protocol/v3"
)

func TestControlSequenceRejectsReplayAndGaps(t *testing.T) {
	for _, seq := range []uint64{1, 3} {
		var buf bytes.Buffer
		for _, s := range []uint64{1, seq} {
			if e := v3.Write(&buf, v3.Frame{Type: v3.Ping, Sequence: s, Payload: v3.Encode(v3.Control{})}); e != nil {
				t.Fatal(e)
			}
		}
		w := wire{r: &buf}
		if _, _, e := w.read(); e != nil {
			t.Fatal(e)
		}
		if _, _, e := w.read(); e == nil {
			t.Fatal("duplicate/out-of-order control accepted")
		}
	}
}

func TestBlockedPixelSocketDoesNotSerializeControl(t *testing.T) {
	pixelClient, pixelServer := net.Pipe()
	controlClient, controlServer := net.Pipe()
	defer pixelClient.Close()
	defer pixelServer.Close()
	defer controlClient.Close()
	defer controlServer.Close()
	pixels := &wire{c: pixelServer}
	control := &wire{c: controlServer}
	blocked := make(chan error, 1)
	go func() {
		blocked <- pixels.frame(v3.Frame{Type: v3.PixelUpdate, Flags: v3.FullFrame, Payload: make([]byte, v3.MaxPixel)})
	}()
	controlDone := make(chan error, 1)
	go func() { controlDone <- control.send(v3.Pong, v3.ID{}, v3.Control{Ack: 1}) }()
	controlClient.SetReadDeadline(time.Now().Add(time.Second))
	f, e := v3.Read(controlClient, false)
	if e != nil || f.Type != v3.Pong {
		t.Fatal("blocked pixels stalled control", e)
	}
	if e = <-controlDone; e != nil {
		t.Fatal(e)
	}
	select {
	case <-blocked:
		t.Fatal("pixel writer was not backpressured")
	default:
	}
	pixelClient.Close()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("pixel disconnect did not release writer")
	}
}
