package browser

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestBlockedBrowserPipeRespectsDeadline(t *testing.T) {
	r, rw := io.Pipe()
	wr, w := io.Pipe()
	defer rw.Close()
	defer wr.Close()
	c := newCDP(r, w)
	defer c.close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	e := c.call(ctx, "", "Browser.getVersion", map[string]any{}, nil)
	if !errors.Is(e, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatal("stalled browser pipe exceeded deadline", e)
	}
}
func TestConfigurationAndViewportBounds(t *testing.T) {
	base := DefaultConfig()
	if !base.Valid() || !base.Viewport(1920, 1200) {
		t.Fatal("invalid defaults")
	}
	for _, change := range []func(*Config){func(c *Config) { c.MaxSessions = 100 }, func(c *Config) { c.MemoryMB = 1 }, func(c *Config) { c.TotalMemoryMB = c.MemoryMB }, func(c *Config) { c.FPS = 100 }, func(c *Config) { c.MaxPixels = 1 << 30 }, func(c *Config) { c.Bandwidth = 0 }, func(c *Config) { c.Tasks = 10000 }, func(c *Config) { c.Refresh = time.Minute }, func(c *Config) { c.MaxFrameBytes = 1 << 30 }} {
		c := base
		change(&c)
		if c.Valid() {
			t.Fatal("unsafe limits accepted")
		}
	}
	if base.Viewport(1<<30, 1<<30) || base.Viewport(319, 200) || base.Viewport(1920, 1201) {
		t.Fatal("invalid viewport accepted")
	}
}
