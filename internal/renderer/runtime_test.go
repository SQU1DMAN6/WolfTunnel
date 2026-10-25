package renderer

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func archiveFixture(t *testing.T, names []string, symlink bool) (string, Manifest) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fixture.zip")
	f, e := os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	m := Manifest{Files: map[string]string{}}
	for _, name := range names {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0755)
		if symlink {
			h.SetMode(os.ModeSymlink | 0777)
		}
		w, e := z.CreateHeader(h)
		if e != nil {
			t.Fatal(e)
		}
		w.Write([]byte("fixture"))
		sum := sha256.Sum256([]byte("fixture"))
		m.Files[name] = hex.EncodeToString(sum[:])
	}
	if e = z.Close(); e != nil {
		t.Fatal(e)
	}
	f.Close()
	return p, m
}
func TestRendererExtractionAndCompleteIntegrity(t *testing.T) {
	p, m := archiveFixture(t, []string{"engine/chrome", "engine/assets/data"}, false)
	dir := t.TempDir()
	if e := unpack(context.Background(), p, dir, m); e != nil {
		t.Fatal(e)
	}
	if e := verify(context.Background(), dir, m); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(dir, "engine/assets/data")
	if e := os.WriteFile(file, []byte("modified"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := verify(context.Background(), dir, m); !errors.Is(e, ErrIntegrity) {
		t.Fatal("modified asset accepted", e)
	}
}
func TestRendererRejectsTraversalDuplicatesSymlinksAndUnknownFiles(t *testing.T) {
	for _, tc := range []struct {
		names []string
		link  bool
	}{
		{[]string{"../escaped"}, false}, {[]string{"/absolute"}, false}, {[]string{"engine/../escape"}, false}, {[]string{"engine/chrome", "engine/chrome"}, false}, {[]string{"engine/chrome"}, true},
	} {
		p, m := archiveFixture(t, tc.names, tc.link)
		if e := unpack(context.Background(), p, t.TempDir(), m); !errors.Is(e, ErrIntegrity) {
			t.Fatal("unsafe archive accepted", tc.names, e)
		}
	}
	p, m := archiveFixture(t, []string{"engine/chrome"}, false)
	m.Files["missing"] = m.Files["engine/chrome"]
	if e := unpack(context.Background(), p, t.TempDir(), m); !errors.Is(e, ErrIntegrity) {
		t.Fatal("incomplete bundle accepted", e)
	}
}
func TestRendererPinAndCancelledInstall(t *testing.T) {
	m := Lock()
	if len(m.Files) < 2 || m.Files[m.Binary] != m.BinarySHA256 || len(m.ArchiveSHA256) != 64 {
		t.Fatal("incomplete runtime pin")
	}
	bad := filepath.Join(t.TempDir(), "bad.zip")
	os.WriteFile(bad, []byte("untrusted download"), 0600)
	if _, e := Ensure(context.Background(), filepath.Join(t.TempDir(), "cache"), bad); !errors.Is(e, ErrIntegrity) {
		t.Fatal("unpinned archive accepted", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, manifest := archiveFixture(t, []string{"engine/chrome"}, false)
	if e := unpack(ctx, p, t.TempDir(), manifest); !errors.Is(e, context.Canceled) {
		t.Fatal("cancel ignored", e)
	}
}
