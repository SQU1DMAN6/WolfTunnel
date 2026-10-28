package frontend

import "wolftunnel/internal/nodeclient"
import "wolftunnel/internal/transport"

type progressEvent struct {
	generation uint64
	event      transport.Event
	browser    *nodeclient.BrowserStatus
}

func (m *Model) observedClient() nodeclient.Client {
	c := m.Client
	generation := m.Generation
	c.Observer = func(event transport.Event) {
		next := progressEvent{generation: generation, event: event}
		select {
		case m.progress <- next:
		default:
			select {
			case <-m.progress:
			default:
			}
			select {
			case m.progress <- next:
			default:
			}
		}
	}
	c.BrowserObserver = func(status nodeclient.BrowserStatus) {
		next := progressEvent{generation: generation, browser: &status}
		select {
		case m.progress <- next:
		default:
			select {
			case <-m.progress:
			default:
			}
			select {
			case m.progress <- next:
			default:
			}
		}
	}
	return c
}
func (m *Model) drainConnection() bool {
	changed := false
	for {
		select {
		case next := <-m.progress:
			if next.generation == m.Generation {
				if next.browser != nil {
					m.BrowserState = *next.browser
				} else {
					m.Network = next.event
					if m.Channels == nil {
						m.Channels = map[string]transport.Event{}
					}
					m.Channels[next.event.Channel] = next.event
				}
				changed = true
			}
		default:
			return changed
		}
	}
}
