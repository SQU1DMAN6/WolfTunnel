package storage

import (
	"fmt"
	"os"
	"path/filepath"
)

// DataDir follows XDG, using the requested ~/.local/share/wolftunnel by default.
func DataDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" || !filepath.IsAbs(base) {
		home, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "wolftunnel"), nil
}
func StateDir(role string) (string, error) {
	if role != "node" && role != "client" {
		return "", fmt.Errorf("invalid state role")
	}
	base, e := DataDir()
	if e != nil {
		return "", e
	}
	home, e := os.UserHomeDir()
	if e != nil {
		return "", e
	}
	custom := false
	if requested := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(requested) && filepath.Clean(requested) != filepath.Join(home, ".local", "share") {
		custom = true
	}
	return locateState(home, base, role, custom)
}
func locateState(home, base, role string, custom bool) (string, error) {
	if custom {
		return filepath.Join(base, role), nil
	}
	// Preserve existing identities in place. Do not silently enrol a new key when
	// upgrading an existing installation to XDG defaults.
	legacy := ".wolftunnel"
	if role == "client" {
		legacy = ".wolftunnel-client"
	}
	previous := filepath.Join(home, legacy)
	if _, e := os.Lstat(filepath.Join(previous, "config.json")); e == nil {
		return previous, nil
	} else if !os.IsNotExist(e) {
		return "", e
	}
	if role == "node" {
		if _, e := os.Lstat(filepath.Join(base, "config.json")); e == nil {
			return base, nil
		} else if !os.IsNotExist(e) {
			return "", e
		}
	}
	return filepath.Join(base, role), nil
}
func ClientLogDir(root string) (string, error) {
	normal, e := StateDir("client")
	if e != nil {
		return "", e
	}
	a, e := filepath.Abs(root)
	if e != nil {
		return "", e
	}
	b, e := filepath.Abs(normal)
	if e != nil {
		return "", e
	}
	if a == b {
		data, e := DataDir()
		if e != nil {
			return "", e
		}
		return filepath.Join(data, "logs"), nil
	}
	return filepath.Join(root, "logs"), nil
}
