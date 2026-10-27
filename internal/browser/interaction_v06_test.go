package browser

import (
	"context"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"wolftunnel/internal/browsernet"
	v3 "wolftunnel/internal/protocol/v3"
	"wolftunnel/internal/testsite"
)

func TestV06InteractionFixtureAndPinch(t *testing.T) {
	cfg := integrationConfig(t)
	site := &testsite.Site{}
	server := httptest.NewServer(site)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	proxy, e := browsernet.New(ctx, browsernet.Options{Resolver: testResolver{}, Dial: func(c context.Context, n, a string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, n, server.Listener.Addr().String())
	}})
	if e != nil {
		t.Fatal(e)
	}
	b, e := Start(ctx, cfg, proxy)
	if e != nil {
		proxy.Close()
		t.Fatal(e)
	}
	defer b.Close()
	id := v3.NewID().String()
	if e = b.NewPage(ctx, id); e != nil {
		t.Fatal(e)
	}
	if e = b.Select(ctx, id, 900, 650); e != nil {
		t.Fatal(e)
	}
	if e = b.Navigate(ctx, id, "http://fixture.test/interaction"); e != nil {
		t.Fatal(e)
	}
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if evaluate(t, b, ctx, id, "document.readyState") == "complete" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	input := func(i v3.Input) {
		t.Helper()
		if e = b.Input(ctx, i); e != nil {
			t.Fatal(e)
		}
	}
	click := func(x, y float64) {
		input(v3.Input{Kind: "down", X: x, Y: y, Button: 1})
		input(v3.Input{Kind: "up", X: x, Y: y, Button: 1})
	}
	click(50, 50)
	click(50, 135)
	input(v3.Input{Kind: "text", Text: "secret-input ✓ Привет Ελληνικά"})
	if value := evaluate(t, b, ctx, id, "document.getElementById('text').value"); !strings.Contains(value, "Привет") {
		t.Fatal("unicode", value)
	}
	input(v3.Input{Kind: "key_down", Code: "ControlLeft", Key: "Control", Modifiers: 2})
	input(v3.Input{Kind: "key_down", Code: "KeyA", Key: "a", Modifiers: 2})
	input(v3.Input{Kind: "key_up", Code: "KeyA", Key: "a", Modifiers: 2})
	input(v3.Input{Kind: "blur"})
	if got := evaluate(t, b, ctx, id, "document.getElementById('text').selectionEnd.toString()"); got == "0" {
		t.Fatal("selection failed")
	}
	clickElement := func(name string) {
		var x, y float64
		point := evaluate(t, b, ctx, id, "(()=>{const r=document.getElementById('"+name+"').getBoundingClientRect();return (r.x+r.width/2)+' '+(r.y+r.height/2)})()")
		if _, err := fmt.Sscan(point, &x, &y); err != nil {
			t.Fatal(err)
		}
		click(x, y)
	}
	clickElement("choice")
	clickElement("answer")
	input(v3.Input{Kind: "text", Text: "WOLF"})
	clickElement("verify")
	if got := evaluate(t, b, ctx, id, "stats.Verified.toString()"); got != "true" {
		t.Fatal("verification fixture not usable", got)
	}
	input(v3.Input{Kind: "down", X: 50, Y: 245, Button: 1})
	input(v3.Input{Kind: "move", X: 180, Y: 245})
	input(v3.Input{Kind: "up", X: 180, Y: 245, Button: 1})
	if got := evaluate(t, b, ctx, id, "stats.DragMoves.toString()"); got == "0" {
		t.Fatal("drag failed")
	}
	input(v3.Input{Kind: "down", X: 321, Y: 250, Button: 1})
	input(v3.Input{Kind: "move", X: 640, Y: 250})
	input(v3.Input{Kind: "up", X: 640, Y: 250, Button: 1})
	until = time.Now().Add(time.Second)
	for evaluate(t, b, ctx, id, "getSelection().toString()") == "" && time.Now().Before(until) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := evaluate(t, b, ctx, id, "getSelection().toString()"); got == "" {
		t.Fatal("pointer text selection failed", evaluate(t, b, ctx, id, "JSON.stringify(document.getElementById('selection').getBoundingClientRect())"))
	}
	input(v3.Input{Kind: "wheel", X: 130, Y: 380, DeltaX: 35.5, DeltaY: 66.25})
	time.Sleep(120 * time.Millisecond)
	if got := evaluate(t, b, ctx, id, "(stats.ScrollX>0&&stats.ScrollY>0).toString()"); got != "true" {
		t.Fatal("fractional 2D scroll failed", got)
	}
	input(v3.Input{Kind: "pinch", X: 400, Y: 300, Scale: 1.25})
	time.Sleep(150 * time.Millisecond)
	zoom := evaluate(t, b, ctx, id, "(visualViewport.scale>1).toString()")
	if zoom != "true" {
		t.Fatal("pinch did not zoom", zoom)
	}
	input(v3.Input{Kind: "pinch", X: 400, Y: 300, Scale: 0.8})
	if got := evaluate(t, b, ctx, id, "(visualViewport.scale===1).toString()"); got != "true" {
		t.Fatal("pinch reverse did not restore scale")
	}
}
