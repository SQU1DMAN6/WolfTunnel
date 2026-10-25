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
