// Package browserlife centralises deadlines shared by client and node.
package browserlife

import "time"

const (
	Startup        = 90 * time.Second // permits the configured node startup (up to 60 s)
	DisplayConnect = 25 * time.Second // normal 20 s transport budget plus protocol/auth
	DisplayAttach  = 30 * time.Second // display budget plus scheduling/cleanup margin
	FirstFrame     = 15 * time.Second // starts only once a page has been requested
	FrameIdle      = 15 * time.Second // node emits a complete refresh at most every 10 s
	ControlIdle    = 30 * time.Second
	Write          = 5 * time.Second
)
