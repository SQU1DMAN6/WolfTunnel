package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateAtomicState(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	var v struct {
		Version int `json:"version"`
	}
	v.Version = 2
	if err := Write(path, v); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private permissions", err)
	}
	v.Version = 3
	if err = Write(path, v); err != nil {
		t.Fatal(err)
	}
	v.Version = 0
	if err = Read(path, &v); err != nil || v.Version != 3 {
		t.Fatal("atomic replacement", err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatal("temporary state leaked")
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if Read(path, &v) == nil {
		t.Fatal("insecure file accepted")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("missing", path); err != nil {
		t.Fatal(err)
	}
	if Read(path, &v) == nil {
		t.Fatal("symlink accepted")
	}
}
func TestStateCorruption(t *testing.T) {
	for _, data := range []string{`{`, `{"version":2,"secret":1}`, `{"version":2} {}`, `null`} {
		t.Run(data, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(p, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			var v struct {
				Version int `json:"version"`
			}
			err := Read(p, &v)
			if err == nil {
				t.Fatal("corrupt file accepted")
			}
		})
	}
}
