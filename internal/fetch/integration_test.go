package fetch_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"wolftunnel/internal/fetch"
	"wolftunnel/internal/node"
	"wolftunnel/internal/nodeclient"
)

func TestNativeClientProtocolNodeHTTPPipeline(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final?secret=hidden", 303)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Set-Cookie", "private=1")
		fmt.Fprint(w, "controlled remote response")
	}))
	defer remote.Close()
	s, err := node.Open(filepath.Join(t.TempDir(), "node"))
	if err != nil {
		t.Fatal(err)
	}
	s.Fetcher = fetch.ControlledFetcherForTest(remote.Listener.Addr().String())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("shutdown")
		}
	}()
	c, err := nodeclient.Load(filepath.Join(t.TempDir(), "client"))
	if err != nil {
		t.Fatal(err)
	}
	c.Config.Host = "127.0.0.1"
	c.Config.Port = ln.Addr().(*net.TCPAddr).Port
	c.Config.Fingerprint = s.Fingerprint
	code, err := s.Auth.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Enrol(ctx, code); err != nil {
		t.Fatal(err)
	}
	out, err := c.Fetch(ctx, "complete-pipeline", "http://public.test/start")
	if err != nil || out.Status != 200 || out.Code != "" || out.FinalURL != "http://public.test/final" || out.Redirects != 1 || out.BodyLength != 26 || out.Headers["Set-Cookie"] != "" {
		t.Fatalf("end-to-end: %+v %v", out, err)
	}
}
