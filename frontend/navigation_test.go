package frontend

import (
	"image"
	"strings"
	"testing"
	"time"
)

func TestStatusURLAndPriority(t *testing.T) {
	for _, raw := range []string{"https://user:password@public.test/", "http://[bad"} {
		if statusURL(raw) != "" {
			t.Fatal("credential/malformed URL displayed")
		}
	}
	m := &Model{Connection: "Connected", LinkTarget: "https://public.test/link"}
	m.navigationNotice("tab", "Opening new tab · https://public.test/target", time.Second)
	m.LinkTarget = "https://public.test/unrelated"
	if !strings.HasPrefix(m.navigationStatus(), "Opening new tab") {
		t.Fatal("hover replaced pending navigation")
	}
	m.NavigationUntil = time.Now().Add(-time.Second)
	if m.navigationStatus() != m.LinkTarget {
		t.Fatal("expired navigation status retained")
	}
	m.Connection = "Error"
	if m.navigationStatus() != "" {
		t.Fatal("hover obscured transport error")
	}
	if len(statusURL("https://public.test/"+strings.Repeat("x", 9000))) != 0 {
		t.Fatal("unbounded status")
	}
}
func TestPreparePixelsReuseAndBounds(t *testing.T) {
	source := image.NewYCbCr(image.Rect(0, 0, 640, 480), image.YCbCrSubsampleRatio420)
	first, e := prepareRGBA(source, nil)
	if e != nil {
		t.Fatal(e)
	}
	next, e := prepareRGBA(source, first)
	if e != nil || first != next {
		t.Fatal("conversion buffer not reused")
	}
	if got, _ := prepareRGBA(first, nil); got != first {
		t.Fatal("RGBA image copied redundantly")
	}
}
func BenchmarkPreparePixels(b *testing.B) {
	source := image.NewYCbCr(image.Rect(0, 0, 1920, 1080), image.YCbCrSubsampleRatio420)
	b.Run("old_allocate_each_frame", func(b *testing.B) {
		b.ReportAllocs()
		for n := 0; n < b.N; n++ {
			if _, e := prepareRGBA(source, nil); e != nil {
				b.Fatal(e)
			}
		}
	})
	b.Run("reuse", func(b *testing.B) {
		dst, _ := prepareRGBA(source, nil)
		b.ReportAllocs()
		b.ResetTimer()
		for n := 0; n < b.N; n++ {
			if _, e := prepareRGBA(source, dst); e != nil {
				b.Fatal(e)
			}
		}
	})
}
