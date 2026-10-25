package node

import (
	"path/filepath"
	"testing"
)

func TestSavedNodeDefaults(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	s, fresh, e := LoadSettings(root)
	if e != nil || !fresh || !s.ManagedRenderer || s.InvitationAddress() != "127.0.0.1:13300" {
		t.Fatal(s, e)
	}
	s.Listen = "127.0.0.1:13300"
	s.PublicAddress = "node.example:13300"
	if e = s.Save(root); e != nil {
		t.Fatal(e)
	}
	s, fresh, e = LoadSettings(root)
	if e != nil || fresh || s.InvitationAddress() != "node.example:13300" {
		t.Fatal(s, e)
	}
}
