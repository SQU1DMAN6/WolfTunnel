package frontend

import "wolftunnel/internal/nodeclient"
import "wolftunnel/internal/transport"

type progressEvent struct {
	generation uint64
	event      transport.Event
}

func (m *Model) observedClient() nodeclient.Client {
	c := m.Client
	generation := m.Generation
	c.Observer = func(event transport.Event) {
		next := progressEvent{generation, event}
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
				m.Network = next.event
				changed = true
			}
		default:
			return changed
		}
	}
}
