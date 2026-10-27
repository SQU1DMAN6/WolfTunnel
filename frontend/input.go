package frontend

import (
	"math"
	"strings"
	"unicode"

	v3 "wolftunnel/internal/protocol/v3"
)

type viewRect struct {
	X, Y, W, H       float64
	RemoteW, RemoteH int
}

func localShortcut(key int) bool {
	return key == 'l' || key == 't' || key == 'w' || key == 'q' || key == ',' || key == 'r'
}

func (r viewRect) CSS(x, y float64) (float64, float64, bool) {
	if r.W <= 0 || r.H <= 0 || r.RemoteW <= 0 || r.RemoteH <= 0 || math.IsNaN(x) || math.IsNaN(y) {
		return 0, 0, false
	}
	inside := x >= r.X && y >= r.Y && x < r.X+r.W && y < r.Y+r.H
	return max(0, min(float64(r.RemoteW)-1, (x-r.X)*float64(r.RemoteW)/r.W)), max(0, min(float64(r.RemoteH)-1, (y-r.Y)*float64(r.RemoteH)/r.H)), inside
}
func (r viewRect) Local(x, y float64) (float64, float64) {
	return r.X + x*r.W/float64(r.RemoteW), r.Y + y*r.H/float64(r.RemoteH)
}
func physicalKey(scan string, key int) (string, string) {
	names := map[string][2]string{"Return": {"Enter", "Enter"}, "Enter": {"Enter", "Enter"}, "Escape": {"Escape", "Escape"}, "Backspace": {"Backspace", "Backspace"}, "Tab": {"Tab", "Tab"}, "Space": {"Space", " "}, "Left": {"ArrowLeft", "ArrowLeft"}, "Right": {"ArrowRight", "ArrowRight"}, "Up": {"ArrowUp", "ArrowUp"}, "Down": {"ArrowDown", "ArrowDown"}, "Home": {"Home", "Home"}, "End": {"End", "End"}, "PageUp": {"PageUp", "PageUp"}, "PageDown": {"PageDown", "PageDown"}, "Delete": {"Delete", "Delete"}, "Insert": {"Insert", "Insert"}, "Left Shift": {"ShiftLeft", "Shift"}, "Right Shift": {"ShiftRight", "Shift"}, "Left Ctrl": {"ControlLeft", "Control"}, "Right Ctrl": {"ControlRight", "Control"}, "Left Alt": {"AltLeft", "Alt"}, "Right Alt": {"AltRight", "Alt"}, "Left GUI": {"MetaLeft", "Meta"}, "Right GUI": {"MetaRight", "Meta"}, "-": {"Minus", "-"}, "=": {"Equal", "="}, "[": {"BracketLeft", "["}, "]": {"BracketRight", "]"}, "\\": {"Backslash", "\\"}, ";": {"Semicolon", ";"}, "'": {"Quote", "'"}, ",": {"Comma", ","}, ".": {"Period", "."}, "/": {"Slash", "/"}, "`": {"Backquote", "`"}}
	if n, ok := names[scan]; ok {
		keyValue := n[1]
		if len([]rune(keyValue)) == 1 && key >= 32 && key <= unicode.MaxRune && !unicode.IsControl(rune(key)) {
			keyValue = string(rune(key))
		}
		return n[0], keyValue
	}
	if len(scan) == 1 {
		ch := scan[0]
		if ch >= 'A' && ch <= 'Z' {
			text := strings.ToLower(scan)
			if key >= 32 && key <= unicode.MaxRune && !unicode.IsControl(rune(key)) {
				text = string(rune(key))
			}
			return "Key" + scan, text
		}
		if ch >= '0' && ch <= '9' {
			value := scan
			if key >= 32 && key <= unicode.MaxRune && !unicode.IsControl(rune(key)) {
				value = string(rune(key))
			}
			return "Digit" + scan, value
		}
	}
	if len(scan) >= 2 && scan[0] == 'F' {
		if (v3.Input{Kind: "key_down", Code: scan}).Validate(320, 200) == nil {
			return scan, scan
		}
	}
	return "", ""
}
