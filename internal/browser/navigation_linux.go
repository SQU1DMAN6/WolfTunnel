//go:build linux

package browser

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"wolftunnel/internal/browsernet"
	v3 "wolftunnel/internal/protocol/v3"
)

type hoverPoint struct {
	epoch uint64
	tab   string
	x, y  float64
}

func (b *Browser) EnableNavigation(enabled bool) { b.mu.Lock(); b.navigation = enabled; b.mu.Unlock() }
func (b *Browser) navigationOn() bool            { b.mu.Lock(); defer b.mu.Unlock(); return b.navigation }

// Resolve only the element actually hit in this browser page. The fixed function
// has no client-supplied source and returns data, never generic CDP authority.
func (b *Browser) linkAt(ctx context.Context, p *Page, x, y float64) (string, error) {
	var node struct {
		Backend int    `json:"backendNodeId"`
		Frame   string `json:"frameId"`
	}
	if e := b.cdp.call(ctx, p.CDP, "DOM.getNodeForLocation", map[string]any{"x": int(x), "y": int(y), "includeUserAgentShadowDOM": false}, &node); e != nil {
		return "", e
	}
	b.mu.Lock()
	world := p.LinkWorld
	if p.LinkFrame != node.Frame {
		world = 0
	}
	b.mu.Unlock()
	if world == 0 {
		var isolated struct {
			ID int `json:"executionContextId"`
		}
		if e := b.cdp.call(ctx, p.CDP, "Page.createIsolatedWorld", map[string]any{"frameId": node.Frame, "worldName": "wolftunnel-link-status"}, &isolated); e != nil {
			return "", e
		}
		world = isolated.ID
		b.mu.Lock()
		p.LinkFrame = node.Frame
		p.LinkWorld = world
		b.mu.Unlock()
	}
	var object struct {
		Object struct {
			ID string `json:"objectId"`
		} `json:"object"`
	}
	if e := b.cdp.call(ctx, p.CDP, "DOM.resolveNode", map[string]any{"backendNodeId": node.Backend, "executionContextId": world}, &object); e != nil {
		return "", e
	}
	defer b.cdp.call(ctx, p.CDP, "Runtime.releaseObject", map[string]string{"objectId": object.Object.ID}, nil)
	var result struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	const function = `function(){let n=this.nodeType===1?this:this.parentElement;while(n){if((n.localName==='a'||n.localName==='area')&&n.hasAttribute('href'))return n.href||'';n=n.parentElement}return ''}`
	if e := b.cdp.call(ctx, p.CDP, "Runtime.callFunctionOn", map[string]any{"objectId": object.Object.ID, "functionDeclaration": function, "returnByValue": true, "silent": true}, &result); e != nil {
		return "", e
	}
	if len(result.Result.Value) > 8192 {
		return "", errors.New("NODE_POLICY_REJECTED")
	}
	return result.Result.Value, nil
}
func (b *Browser) hover() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
			var point hoverPoint
			select {
			case point = <-b.hoverPoints:
			default:
				continue
			}
			if !b.navigationOn() {
				continue
			}
			active, _, _ := b.Active()
			if active != point.tab {
				continue
			}
			p, e := b.page(active)
			if e != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(b.ctx, 500*time.Millisecond)
			target, e := b.linkAt(ctx, p, point.x, point.y)
			cancel()
			if e != nil {
				target = ""
			}
			if browsernet.Validate(target) != nil {
				target = ""
			}
			b.mu.Lock()
			changed := point.epoch == b.hoverEpoch && target != b.hoverTarget
			if point.epoch == b.hoverEpoch {
				b.hoverTarget = target
			}
			b.mu.Unlock()
			if changed {
				if !b.emit(Event{Kind: "hover", Tab: active, State: "Hover", Target: target}) {
					b.mu.Lock()
					b.hoverTarget = "\x00"
					b.mu.Unlock()
					select {
					case b.hoverPoints <- point:
					default:
					}
				}
			}
		}
	}
}
func (b *Browser) queueHover(tab string, i v3.Input) {
	b.mu.Lock()
	epoch := b.hoverEpoch
	b.mu.Unlock()
	p := hoverPoint{epoch: epoch, tab: tab, x: i.X, y: i.Y}
	select {
	case b.hoverPoints <- p:
	default:
		select {
		case <-b.hoverPoints:
		default:
		}
		select {
		case b.hoverPoints <- p:
		default:
		}
	}
}
func (b *Browser) newTab(tab, target string, gesture bool) {
	if !b.navigationOn() {
		return
	}
	code := ""
	if !gesture {
		code = "POPUP_BLOCKED"
	} else if browsernet.Validate(target) != nil {
		code = "NODE_POLICY_REJECTED"
	}
	if len(target) > 8192 || target == "" {
		target = "about:blank"
		code = "NODE_POLICY_REJECTED"
	}
	// Credentials must never be sent back for status display. Validation also
	// rejects schemes/private literal addresses before any tab allocation.
	if code != "" {
		b.emit(Event{Kind: "newtab", Tab: tab, Target: "about:blank", State: "Rejected", Code: code})
		return
	}
	b.emit(Event{Kind: "newtab", Tab: tab, Target: target, State: "NewTabRequested"})
}
func (b *Browser) windowOpen(tab string, raw json.RawMessage) {
	var event struct {
		URL         string `json:"url"`
		UserGesture bool   `json:"userGesture"`
	}
	if json.Unmarshal(raw, &event) == nil {
		b.newTab(tab, event.URL, event.UserGesture)
	}
}
