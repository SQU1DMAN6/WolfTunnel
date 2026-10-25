package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
)

var ErrBrowser = errors.New("BROWSER_CRASHED")

type packet struct {
	ID      uint64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Session string          `json:"sessionId,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code int `json:"code"`
	} `json:"error,omitempty"`
}
type cdp struct {
	r       io.ReadCloser
	w       io.WriteCloser
	mu      sync.Mutex
	write   sync.Mutex
	next    atomic.Uint64
	pending map[uint64]chan packet
	events  chan packet
	done    chan struct{}
	once    sync.Once
}

func newCDP(r io.ReadCloser, w io.WriteCloser) *cdp {
	c := &cdp{r: r, w: w, pending: make(map[uint64]chan packet), events: make(chan packet, 128), done: make(chan struct{})}
	go c.read()
	return c
}
func (c *cdp) close() { c.once.Do(func() { close(c.done); c.r.Close(); c.w.Close() }) }
func (c *cdp) read() {
	defer c.close()
	r := bufio.NewReaderSize(c.r, 64<<10)
	for {
		var b []byte
		for {
			part, e := r.ReadSlice(0)
			if len(b)+len(part) > 16<<20 {
				return
			}
			b = append(b, part...)
			if e == nil {
				break
			}
			if e != bufio.ErrBufferFull {
				return
			}
		}
		var p packet
		if len(b) == 0 || json.Unmarshal(b[:len(b)-1], &p) != nil {
			return
		}
		if p.ID != 0 {
			c.mu.Lock()
			ch := c.pending[p.ID]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- p:
				default:
				}
			}
		} else {
			switch p.Method {
			case "Page.loadEventFired", "Page.frameNavigated", "Network.responseReceived", "Network.loadingFailed", "Target.targetCrashed", "Inspector.targetCrashed", "Target.targetCreated":
				select {
				case c.events <- p:
				case <-c.done:
					return
				default:
					if p.Method == "Target.targetCreated" {
						return
					}
				}
			}
		}
	}
}
func (c *cdp) call(ctx context.Context, session, method string, params any, out any) error {
	id := c.next.Add(1)
	raw, e := json.Marshal(params)
	if e != nil {
		return ErrBrowser
	}
	p := packet{ID: id, Session: session, Method: method, Params: raw}
	b, e := json.Marshal(p)
	if e != nil || len(b) > 1<<20 {
		return ErrBrowser
	}
	ch := make(chan packet, 1)
	c.mu.Lock()
	if len(c.pending) >= 32 {
		c.mu.Unlock()
		return errors.New("RESOURCE_LIMIT")
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	// A dead browser can stop reading its pipe. A request deadline must also
	// unblock a pipe write (and callers queued behind it), not just response wait.
	stopWrite := context.AfterFunc(ctx, c.close)
	c.write.Lock()
	_, e = c.w.Write(append(b, 0))
	c.write.Unlock()
	stopWrite()
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrBrowser
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return ErrBrowser
	case resp := <-ch:
		if resp.Error != nil {
			return &commandError{method: method, code: resp.Error.Code}
		}
		if out != nil && json.Unmarshal(resp.Result, out) != nil {
			return ErrBrowser
		}
		return nil
	}
}

type commandError struct {
	method string
	code   int
}

func (e *commandError) Error() string { return "BROWSER_CRASHED" }
