// Package pixelstream validates independent, bounded JPEG/PNG viewport updates.
package pixelstream

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"time"

	v3 "wolftunnel/internal/protocol/v3"
)

var ErrPixel = errors.New("PIXEL_ERROR")

const MaxPixels = 3840 * 2160
const Prefix = 48

type Update struct {
	Tab           v3.ID
	Width, Height int
	Encoding      byte
	Captured      time.Time
	InputSequence uint64
	CaptureUS     uint32
	Data          []byte
}

func (u Update) Encode() ([]byte, error) {
	if !valid(u.Width, u.Height) || len(u.Data) > v3.MaxPixel-Prefix || (u.Encoding != 1 && u.Encoding != 2) {
		return nil, ErrPixel
	}
	p := make([]byte, Prefix+len(u.Data))
	p[0] = u.Encoding
	binary.BigEndian.PutUint32(p[4:8], uint32(u.Width))
	binary.BigEndian.PutUint32(p[8:12], uint32(u.Height))
	binary.BigEndian.PutUint64(p[12:20], uint64(u.Captured.UnixNano()))
	binary.BigEndian.PutUint64(p[20:28], u.InputSequence)
	binary.BigEndian.PutUint32(p[28:32], u.CaptureUS)
	copy(p[32:48], u.Tab[:])
	copy(p[Prefix:], u.Data)
	return p, nil
}
func Parse(p []byte) (Update, error) {
	if len(p) < Prefix || len(p) > v3.MaxPixel || p[1] != 0 || p[2] != 0 || p[3] != 0 {
		return Update{}, ErrPixel
	}
	u := Update{Encoding: p[0], Width: int(binary.BigEndian.Uint32(p[4:8])), Height: int(binary.BigEndian.Uint32(p[8:12])), Captured: time.Unix(0, int64(binary.BigEndian.Uint64(p[12:20]))), InputSequence: binary.BigEndian.Uint64(p[20:28]), CaptureUS: binary.BigEndian.Uint32(p[28:32]), Data: p[Prefix:]}
	copy(u.Tab[:], p[32:48])
	if !valid(u.Width, u.Height) || (u.Encoding != 1 && u.Encoding != 2) {
		return Update{}, ErrPixel
	}
	cfg, format, e := image.DecodeConfig(bytes.NewReader(u.Data))
	if e != nil || cfg.Width != u.Width || cfg.Height != u.Height || (u.Encoding == 1 && format != "jpeg") || (u.Encoding == 2 && format != "png") {
		return Update{}, ErrPixel
	}
	return u, nil
}
func valid(w, h int) bool {
	return w >= 320 && h >= 200 && w <= 3840 && h <= 2160 && int64(w)*int64(h) <= MaxPixels
}
func Decode(p []byte) (Update, image.Image, error) {
	u, e := Parse(p)
	if e != nil {
		return u, nil, e
	}
	img, _, e := image.Decode(bytes.NewReader(u.Data))
	if e != nil {
		return u, nil, ErrPixel
	}
	return u, img, nil
}
