package nodeclient

import (
	"context"
	"testing"
	v3 "wolftunnel/internal/protocol/v3"
)

func TestInputQueueCoalescesMovementAndPreservesUrgentEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &Browser{ctx: ctx, cancel: cancel, queue: make(chan transmit, 128), wake: make(chan struct{}, 1), Events: make(chan BrowserEvent, 1), width: 1920, height: 1080}
	id := v3.NewID().String()
	for n := 0; n < 10000; n++ {
		if e := b.Input(id, v3.Input{Kind: "move", X: float64(n % 1920), Y: 20}); e != nil {
			t.Fatal(e)
		}
	}
	if len(b.queue) != 0 || len(b.wake) != 1 || b.movement == nil {
		t.Fatal("movement was not coalesced")
	}
	for _, kind := range []string{"down", "up"} {
		if e := b.Input(id, v3.Input{Kind: kind, Button: 1, X: 50, Y: 50}); e != nil {
			t.Fatal(e)
		}
	}
	if b.movement != nil {
		t.Fatal("old pointer move would replay after click")
	}
	for _, kind := range []string{"down", "up"} {
		if kind == "down" {
			if got := <-b.queue; got.body.Input.Kind != "move" {
				t.Fatal("latest pointer position was not delivered before down")
			}
		}
		if got := <-b.queue; got.body.Input.Kind != kind {
			t.Fatal("button order changed")
		}
	}
	for n := 0; n < 128; n++ {
		if e := b.Input(id, v3.Input{Kind: "key_down", Code: "KeyA", Key: "a"}); e != nil {
			t.Fatal(e)
		}
	}
	if e := b.Input(id, v3.Input{Kind: "key_up", Code: "KeyA", Key: "a"}); e == nil || e.Error() != "RESOURCE_LIMIT" {
		t.Fatal("unbounded urgent input queue", e)
	}
	if ctx.Err() == nil {
		t.Fatal("overflow silently lost release instead of closing session")
	}
}

func TestBurstDragResizeAndOldNodeCapability(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &Browser{ctx: ctx, cancel: cancel, queue: make(chan transmit, 128), wake: make(chan struct{}, 1), Events: make(chan BrowserEvent, 1), width: 900, height: 650}
	id := v3.NewID().String()
	if e := b.Input(id, v3.Input{Kind: "down", X: 20, Y: 20, Button: 1}); e != nil {
		t.Fatal(e)
	}
	for n := 0; n < 1000; n++ {
		if e := b.Input(id, v3.Input{Kind: "move", X: float64(n % 800), Y: 50}); e != nil {
			t.Fatal(e)
		}
	}
	b.Input(id, v3.Input{Kind: "up", X: 199, Y: 50, Button: 1})
	for _, kind := range []string{"down", "move", "up"} {
		got := <-b.queue
		if got.body.Input.Kind != kind {
			t.Fatal("drag reorder", kind, got.body.Input.Kind)
		}
	}
	if b.Metrics().MovesCoalesced != 999 {
		t.Fatal("moves not coalesced")
	}
	b.selectedTab = id
	for n := 0; n < 500; n++ {
		if e := b.Resize(800+n%100, 600); e != nil {
			t.Fatal(e)
		}
	}
	if len(b.queue) != 1 || b.resize == nil || b.resize.body.Width != 899 || b.Metrics().ResizeCoalesced != 499 {
		t.Fatal("resize burst unbounded")
	}
	if got := <-b.queue; got.body.Input.Kind != "blur" {
		t.Fatal("held input not cleared before resize")
	}
	if e := b.Input(id, v3.Input{Kind: "pinch", X: 30, Y: 30, Scale: 1.2}); e == nil || e.Error() != "UNSUPPORTED_INPUT" {
		t.Fatal("new gesture sent to old node", e)
	}
	b.resizing = false
	if e := b.Input(id, v3.Input{Kind: "key_down", Code: "KeyA", Key: "a", Repeat: true}); e != nil {
		t.Fatal(e)
	}
	if got := <-b.queue; got.body.Input.Repeat {
		t.Fatal("old strict schema got repeat extension")
	}
	b.fail("PIXEL_STREAM_FAILED")
	if got := <-b.Events; got.Control.Code != "PIXEL_STREAM_FAILED" {
		t.Fatal("local stream diagnosis lost")
	}
}

func TestResizeBackToOriginalWaitsForLatestAcknowledgement(t *testing.T) {
	b := &Browser{resizing: true, wantedW: 900, wantedH: 650, resizeInFlight: true}
	if b.acceptViewportFrame(900, 650) || !b.Resizing() {
		t.Fatal("old same-size frame resumed input before acknowledgement")
	}
	b.resizeInFlight = false
	b.resize = &transmit{typ: v3.ViewportSet, body: v3.Control{Width: 900, Height: 650}}
	if b.acceptViewportFrame(900, 650) {
		t.Fatal("pending replacement resize ignored")
	}
	b.resize = nil
	if b.acceptViewportFrame(800, 600) {
		t.Fatal("intermediate image accepted")
	}
	if !b.acceptViewportFrame(900, 650) || b.Resizing() {
		t.Fatal("matching acknowledged image did not resume input")
	}
}
