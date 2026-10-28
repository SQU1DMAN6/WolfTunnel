package browser

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"wolftunnel/internal/browsernet"
	v3 "wolftunnel/internal/protocol/v3"
	"wolftunnel/internal/testsite"
)

func TestLinkHoverCtrlClickAndBrowserWindow(t *testing.T) {
	cfg := integrationConfig(t)
	site := httptest.NewServer(&testsite.Site{})
	defer site.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	proxy, e := browsernet.New(ctx, browsernet.Options{Resolver: testResolver{}, Dial: func(c context.Context, n, a string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, n, site.Listener.Addr().String())
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
	b.EnableNavigation(true)
	tab := v3.NewID().String()
	if e = b.NewPage(ctx, tab); e != nil {
		t.Fatal(e)
	}
	if e = b.Select(ctx, tab, 900, 650); e != nil {
		t.Fatal(e)
	}
	load := func() {
		t.Helper()
		if e = b.Navigate(ctx, tab, "http://fixture.test/links"); e != nil {
			t.Fatal(e)
		}
		until := time.Now().Add(3 * time.Second)
		for evaluate(t, b, ctx, tab, "document.readyState") != "complete" && time.Now().Before(until) {
			time.Sleep(10 * time.Millisecond)
		}
	}
	load()
	evaluate(t, b, ctx, tab, "location.hash='section';'changed'")
	fragmentDeadline := time.NewTimer(2 * time.Second)
fragment:
	for {
		select {
		case e := <-b.Events:
			if e.State == "Completed" && strings.HasSuffix(e.Target, "#section") {
				break fragment
			}
		case <-fragmentDeadline.C:
			t.Fatal("same-document navigation was not reported")
		}
	}
	fragmentDeadline.Stop()
	load()
	input := func(i v3.Input) {
		t.Helper()
		if e = b.Input(ctx, i); e != nil {
			t.Fatal(e)
		}
	}
	event := func(kind string) Event {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case e := <-b.Events:
				if e.Kind == kind {
					return e
				}
			case <-timer.C:
				t.Fatal("missing browser event", kind)
				return Event{}
			}
		}
	}
	input(v3.Input{Kind: "move", X: 70, Y: 35})
	if e := event("hover"); e.Target != "http://fixture.test/destination?case=normal" {
		t.Fatal(e)
	}
	for _, test := range []struct {
		y       float64
		want    string
		control bool
	}{{35, "http://fixture.test/destination?case=normal", true}, {95, "http://fixture.test/destination?case=blank", true}, {155, "http://fixture.test/link-redirect", true}, {395, "http://fixture.test/destination?case=nested", true}, {455, "http://second.fixture.test/destination?case=external", true}, {95, "http://fixture.test/destination?case=blank", false}, {215, "http://fixture.test/destination?case=script", false}} {
		mods := 0
		if test.control {
			mods = 2
		}
		input(v3.Input{Kind: "down", X: 70, Y: test.y, Button: 1, Modifiers: mods})
		input(v3.Input{Kind: "up", X: 70, Y: test.y, Button: 1})
		e := event("newtab")
		if e.Target != test.want || e.Code != "" {
			t.Fatalf("new-tab request %+v expected %s", e, test.want)
		}
		if location := evaluate(t, b, ctx, tab, "location.pathname"); location != "/links" {
			t.Fatal("origin navigated", location)
		}
	}
	evaluate(t, b, ctx, tab, "window.open('/destination?case=unprompted','_blank');'requested'")
	if e := event("newtab"); e.Code != "POPUP_BLOCKED" {
		t.Fatal("unprompted popup not classified", e)
	}
	input(v3.Input{Kind: "down", X: 70, Y: 275, Button: 1, Modifiers: 2})
	input(v3.Input{Kind: "up", X: 70, Y: 275, Button: 1})
	if e := event("newtab"); e.Code != "NODE_POLICY_REJECTED" {
		t.Fatal(e)
	}
	input(v3.Input{Kind: "down", X: 800, Y: 550, Button: 1, Modifiers: 2})
	input(v3.Input{Kind: "up", X: 800, Y: 550, Button: 1})
	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
waiting:
	for {
		select {
		case e := <-b.Events:
			if e.Kind == "newtab" {
				t.Fatal("non-link/duplicate produced tab", e)
			}
		case <-timer.C:
			break waiting
		}
	}
	input(v3.Input{Kind: "down", X: 70, Y: 35, Button: 1})
	input(v3.Input{Kind: "up", X: 70, Y: 35, Button: 1})
	until := time.Now().Add(3 * time.Second)
	for evaluate(t, b, ctx, tab, "location.pathname") != "/destination" && time.Now().Before(until) {
		time.Sleep(10 * time.Millisecond)
	}
	if path := evaluate(t, b, ctx, tab, "location.pathname"); path != "/destination" {
		t.Fatal("ordinary click did not navigate", path)
	}
}

func TestCriticalNewTabEventOverflowIsExplicit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &Browser{ctx: ctx, cancel: cancel, Events: make(chan Event, 1)}
	b.Events <- Event{State: "Loading"}
	if b.emit(Event{Kind: "hover", State: "Hover"}) || ctx.Err() != nil {
		t.Fatal("obsolete hover should be replaceable")
	}
	if b.emit(Event{Kind: "newtab", State: "NewTabRequested"}) || ctx.Err() == nil || b.TerminalCode() != "RESOURCE_LIMIT" {
		t.Fatal("critical navigation silently lost")
	}
}
