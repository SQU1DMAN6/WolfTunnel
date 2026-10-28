package nodeclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wolftunnel/internal/identity"
	"wolftunnel/internal/pixelstream"
	v3 "wolftunnel/internal/protocol/v3"
)

func TestFrameFailureBoundaries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	os.Mkdir(root, 0700)
	cert, e := identity.Node(root)
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"first_timeout", "control_header", "bad_header", "truncated", "decode", "dimensions"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ln, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			defer ln.Close()
			id := v3.NewID()
			done := make(chan struct{})
			go func() {
				defer close(done)
				raw, e := ln.Accept()
				if e != nil {
					return
				}
				defer raw.Close()
				stop := context.AfterFunc(ctx, func() { raw.Close() })
				defer stop()
				peer := tls.Server(raw, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}})
				if peer.HandshakeContext(ctx) != nil {
					return
				}
				if kind == "first_timeout" {
					<-ctx.Done()
					return
				}
				payload := make([]byte, pixelstream.Prefix+8)
				payload[0] = 1
				binary.BigEndian.PutUint32(payload[4:8], 800)
				binary.BigEndian.PutUint32(payload[8:12], 600)
				if kind == "dimensions" {
					binary.BigEndian.PutUint32(payload[4:8], 9000000)
				}
				var data bytes.Buffer
				v3.Write(&data, v3.Frame{Type: v3.PixelUpdate, Flags: v3.FullFrame, Sequence: 1, Session: id, Payload: payload})
				p := data.Bytes()
				if kind == "bad_header" || kind == "control_header" {
					p[0] = 'X'
				}
				if kind == "truncated" {
					p = p[:len(p)-4]
				}
				peer.Write(p)
			}()
			c, e := Load(filepath.Join(t.TempDir(), "client"))
			if e != nil {
				t.Fatal(e)
			}
			c.Config.Port = ln.Addr().(*net.TCPAddr).Port
			c.Config.Fingerprint = identity.Fingerprint(cert.Certificate[0])
			conn, e := c.dial(ctx, c.attempt("pixel"))
			if e != nil {
				t.Fatal(e)
			}
			defer conn.Close()
			b := &Browser{ID: id, ctx: ctx, cancel: cancel, pixel: &browserPeer{conn: conn}, Events: make(chan BrowserEvent, 1), frameRequested: true, firstTimeout: 80 * time.Millisecond, width: 800, height: 600}
			b.wg.Add(1)
			if kind == "control_header" {
				b.control = b.pixel
				go b.readControl()
			} else {
				go b.readPixels()
			}
			b.wg.Wait()
			want := map[string]string{"control_header": "CONTROL_PROTOCOL_INVALID", "first_timeout": "FIRST_FRAME_TIMEOUT", "bad_header": "DISPLAY_PROTOCOL_INVALID", "truncated": "DISPLAY_FRAME_TRUNCATED", "decode": "FRAME_DECODE_FAILED", "dimensions": "FRAME_DECODE_FAILED"}[kind]
			select {
			case event := <-b.Events:
				if event.Control.Code != want {
					t.Fatal("failure boundary", event.Control.Code, want)
				}
			default:
				t.Fatal("no precise error")
			}
			<-done
		})
	}
}
