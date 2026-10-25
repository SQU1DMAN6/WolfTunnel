package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/draw"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wolftunnel/internal/browsernet"
	v3 "wolftunnel/internal/protocol/v3"
)

func TestCaptureEncodingComparison(t *testing.T) {
	cfg := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<!doctype html><style>body{font:16px sans-serif;background:#eef3fa}main{background:white;padding:32px;margin:30px;width:1000px}button{padding:15px}</style><main><h1>Remote browser text quality</h1><p>Fine text, forms, borders and ordinary page content. 0123456789</p><button>Interactive control</button><hr><p>Lossy capture compared with lossless and a small changed tile.</p></main>`)
	}))
	defer fixture.Close()
	p, e := browsernet.New(ctx, browsernet.Options{Resolver: testResolver{}, Dial: func(ctx context.Context, n, a string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, n, fixture.Listener.Addr().String())
	}})
	if e != nil {
		t.Fatal(e)
	}
	b, e := Start(ctx, cfg, p)
	if e != nil {
		p.Close()
		t.Fatal(e)
	}
	defer b.Close()
	tab := v3.NewID().String()
	if e = b.NewPage(ctx, tab); e != nil {
		t.Fatal(e)
	}
	if e = b.Select(ctx, tab, 1920, 1080); e != nil {
		t.Fatal(e)
	}
	if e = b.Navigate(ctx, tab, "http://fixture.test/encoding"); e != nil {
		t.Fatal(e)
	}
	time.Sleep(100 * time.Millisecond)
	page, _ := b.page(tab)
	report := map[string]any{"viewport": "1920x1080", "quality": cfg.Quality, "capture_samples_per_encoding": 8, "tile_size": 64, "note": "CDP captures include paint/capture/encoding; synthetic tile experiment includes full-frame decode/diff/re-encode, excludes capture already required by both paths"}
	var original []byte
	for _, format := range []string{"jpeg", "png"} {
		var elapsed time.Duration
		var size int
		for n := 0; n < 8; n++ {
			params := map[string]any{"format": format, "fromSurface": true, "captureBeyondViewport": false}
			if format == "jpeg" {
				params["quality"] = cfg.Quality
			}
			var out struct {
				Data string `json:"data"`
			}
			start := time.Now()
			if e = b.cdp.call(ctx, page.CDP, "Page.captureScreenshot", params, &out); e != nil {
				t.Fatal(e)
			}
			elapsed += time.Since(start)
			raw, e := base64.StdEncoding.DecodeString(out.Data)
			if e != nil {
				t.Fatal(e)
			}
			size = len(raw)
			if format == "jpeg" {
				original = raw
			}
		}
		report[format+"_capture_encode_mean_ms"] = float64(elapsed.Microseconds()) / 8000
		report[format+"_bytes"] = size
	}
	// Isolate the added cost of a safe, reconstructable dirty-tile strategy. This
	// experiment changes one 64px rectangle; it is not a production tile codec.
	decoded, e := jpeg.Decode(bytes.NewReader(original))
	if e != nil {
		t.Fatal(e)
	}
	source := image.NewRGBA(decoded.Bounds())
	draw.Draw(source, source.Bounds(), decoded, image.Point{}, draw.Src)
	// Both compared captures must use the same codec generation; otherwise a
	// JPEG round-trip would create artificial changes throughout the page.
	var baseline bytes.Buffer
	if e = jpeg.Encode(&baseline, source, &jpeg.Options{Quality: cfg.Quality}); e != nil {
		t.Fatal(e)
	}
	baseImage, e := jpeg.Decode(bytes.NewReader(baseline.Bytes()))
	if e != nil {
		t.Fatal(e)
	}
	before := image.NewRGBA(source.Bounds())
	draw.Draw(before, before.Bounds(), baseImage, image.Point{}, draw.Src)
	changed := image.NewRGBA(source.Bounds())
	copy(changed.Pix, source.Pix)
	draw.Draw(changed, image.Rect(128, 128, 192, 192), image.NewUniform(image.Black), image.Point{}, draw.Src)
	var whole bytes.Buffer
	if e = jpeg.Encode(&whole, changed, &jpeg.Options{Quality: cfg.Quality}); e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	var total, tiles int
	for n := 0; n < 12; n++ {
		img, e := jpeg.Decode(bytes.NewReader(whole.Bytes()))
		if e != nil {
			t.Fatal(e)
		}
		after := image.NewRGBA(img.Bounds())
		draw.Draw(after, after.Bounds(), img, image.Point{}, draw.Src)
		total, tiles = 0, 0
		for y := 0; y < 1080; y += 64 {
			for x := 0; x < 1920; x += 64 {
				rect := image.Rect(x, y, min(x+64, 1920), min(y+64, 1080))
				dirty := false
				// JPEG is lossy: compare after recompressing the entire changed viewport.
				// This also measures false-positive tiles introduced by recompression.
				for row := rect.Min.Y; row < rect.Max.Y; row++ {
					a := before.PixOffset(rect.Min.X, row)
					z := after.PixOffset(rect.Min.X, row)
					width := rect.Dx() * 4
					if !bytes.Equal(before.Pix[a:a+width], after.Pix[z:z+width]) {
						dirty = true
						break
					}
				}
				if dirty {
					var tile bytes.Buffer
					if e = jpeg.Encode(&tile, after.SubImage(rect), &jpeg.Options{Quality: cfg.Quality}); e != nil {
						t.Fatal(e)
					}
					total += tile.Len()
					tiles++
				}
			}
		}
	}
	report["tile_extra_decode_diff_encode_mean_ms"] = float64(time.Since(start).Microseconds()) / 12000
	report["tile_bytes"] = total
	report["dirty_tiles"] = tiles
	report["full_changed_jpeg_bytes"] = whole.Len()
	raw, e := json.MarshalIndent(report, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	t.Log(string(raw))
	if dir := os.Getenv("WOLF_ARTIFACT_DIR"); dir != "" {
		if e = os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, "encoding-comparison.json"), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
