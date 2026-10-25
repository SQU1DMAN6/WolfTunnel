package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestXDGDefaultsAndLogIsolation(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", base)
	node, e := StateDir("node")
	if e != nil || node != filepath.Join(base, "wolftunnel", "node") {
		t.Fatal(node, e)
	}
	client, e := StateDir("client")
	if e != nil || client != filepath.Join(base, "wolftunnel", "client") {
		t.Fatal(client, e)
	}
	logs, e := ClientLogDir(client)
	if e != nil || logs != filepath.Join(base, "wolftunnel", "logs") {
		t.Fatal(logs, e)
	}
	isolated := filepath.Join(t.TempDir(), "client")
	logs, e = ClientLogDir(isolated)
	if e != nil || logs != filepath.Join(isolated, "logs") {
		t.Fatal("explicit state leaked to real profile", logs, e)
	}
}

func TestDefaultUpgradeKeepsExistingInstallation(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, ".local", "share", "wolftunnel")
	for _, role := range []string{"node", "client"} {
		name := ".wolftunnel"
		if role == "client" {
			name = ".wolftunnel-client"
		}
		legacy := filepath.Join(home, name)
		if e := os.MkdirAll(legacy, 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(legacy, "config.json"), []byte("existing identity"), 0600); e != nil {
			t.Fatal(e)
		}
		got, e := locateState(home, base, role, false)
		if e != nil || got != legacy {
			t.Fatal("upgrade selected a new identity location", got, e)
		}
		got, e = locateState(home, base, role, true)
		if e != nil || got != filepath.Join(base, role) {
			t.Fatal("explicit data override ignored", got, e)
		}
	}
}
