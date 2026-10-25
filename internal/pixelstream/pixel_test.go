package pixelstream

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
	"time"
	v3 "wolftunnel/internal/protocol/v3"
)

func seed() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 320, 200))
	img.Set(5, 5, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 82})
	p, _ := (Update{Tab: v3.NewID(), Width: 320, Height: 200, Encoding: 1, Captured: time.Now(), Data: b.Bytes()}).Encode()
	return p
}
func TestPixelBoundsAndRoundtrip(t *testing.T) {
	p := seed()
	u, img, e := Decode(p)
	if e != nil || img.Bounds().Dx() != 320 || u.Tab.Zero() {
		t.Fatal(e)
	}
	for _, bad := range [][]byte{nil, p[:31], p[:47], append([]byte(nil), p...)} {
		if len(bad) == len(p) {
			binary.BigEndian.PutUint32(bad[4:8], ^uint32(0))
		}
		if _, _, e = Decode(bad); e == nil {
			t.Fatal("invalid image accepted")
		}
	}
	bad := append([]byte(nil), p...)
	bad[0] = 2
	if _, e = Parse(bad); e == nil {
		t.Fatal("wrong encoding accepted")
	}
	bad = append([]byte(nil), p...)
	binary.BigEndian.PutUint32(bad[8:12], 201)
	if _, e = Parse(bad); e == nil {
		t.Fatal("declared dimensions mismatch accepted")
	}
}
func FuzzPixelUpdate(f *testing.F) {
	f.Add(seed())
	f.Add([]byte("PNG"))
	f.Fuzz(func(t *testing.T, p []byte) {
		u, e := Parse(p)
		if e == nil {
			if u.Width*u.Height > MaxPixels {
				t.Fatal("excess pixels")
			}
			if u.Width*u.Height <= 1<<20 {
				_, _, _ = Decode(p)
			}
		}
	})
}
func BenchmarkDecodeViewport(b *testing.B) {
	p := seed()
	b.ReportAllocs()
	b.SetBytes(int64(len(p)))
	for b.Loop() {
		_, _, e := Decode(p)
		if e != nil {
			b.Fatal(e)
		}
	}
}
