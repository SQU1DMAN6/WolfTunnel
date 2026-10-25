package frontend

import (
	"math"
	"testing"
	v3 "wolftunnel/internal/protocol/v3"
)

func TestViewportCoordinatesAndPhysicalKeys(t *testing.T) {
	r := viewRect{X: 50, Y: 100, W: 960, H: 540, RemoteW: 1920, RemoteH: 1080}
	x, y, in := r.CSS(530, 370)
	if !in || x != 960 || y != 540 {
		t.Fatal("bad letterbox mapping")
	}
	lx, ly := r.Local(x, y)
	if lx != 530 || ly != 370 {
		t.Fatal("inverse mapping")
	}
	if _, _, inside := r.CSS(0, 0); inside {
		t.Fatal("letterbox accepted input")
	}
	if _, _, inside := r.CSS(math.NaN(), 0); inside {
		t.Fatal("NaN accepted")
	}
	for _, scan := range []string{"A", "9", "Left", "Return", "Left Ctrl", "Right Shift", "F12", ";"} {
		code, key := physicalKey(scan, 0)
		if code == "" || (v3.Input{Kind: "key_down", Code: code, Key: key}).Validate(1920, 1080) != nil {
			t.Fatalf("unsupported physical key %s", scan)
		}
	}
}
