package renderer

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	m, e := Lock()
	if e != nil {
		t.Fatal(e)
	}
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

func TestPlatformManifests(t *testing.T) {
	for _, platform := range []string{"linux-amd64", "linux-arm64"} {
		t.Run(platform, func(t *testing.T) {
			m, e := ManifestFor(platform)
			if e != nil {
				t.Fatal(e)
			}
			if m.Platform != platform || m.Version == "" || len(m.Files) < 100 || m.Files[m.Binary] != m.BinarySHA256 || len(m.ArchiveSHA256) != 64 {
				t.Fatal("incomplete or wrong architecture manifest")
			}
			for name, digest := range m.Files {
				if !safePath(name) || len(digest) != 64 {
					t.Fatal("invalid manifest entry", name)
				}
			}
			if !strings.HasPrefix(m.URL, "https://storage.googleapis.com/chrome-for-testing-public/"+m.Version+"/") {
				t.Fatal("unexpected runtime origin")
			}
		})
	}
	m, e := Lock()
	if e != nil || m.Platform != runtime.GOOS+"-"+runtime.GOARCH {
		t.Fatal("runtime does not select the executing CPU", e)
	}
	if _, e = ManifestFor("linux-riscv64"); e == nil {
		t.Fatal("unsupported architecture silently fell back")
	}
	if _, e = EnsureFor(context.Background(), filepath.Join(t.TempDir(), "cache"), "", "linux-riscv64"); e == nil {
		t.Fatal("installer accepted an unsupported architecture")
	}
	amd, _ := ManifestFor("linux-amd64")
	arm, _ := ManifestFor("linux-arm64")
	if amd.BinarySHA256 == arm.BinarySHA256 || directory("cache", amd) == directory("cache", arm) {
		t.Fatal("architectures share an engine/cache")
	}
}

// This opt-in test validates the real ARM archive on either host architecture.
func TestARM64RuntimeArchive(t *testing.T) {
	archive := os.Getenv("WOLF_RENDERER_ARCHIVE_ARM64")
	if archive == "" {
		t.Skip("set WOLF_RENDERER_ARCHIVE_ARM64 to the pinned upstream archive")
	}
	base := filepath.Join(t.TempDir(), "cache")
	i, e := EnsureFor(context.Background(), base, archive, "linux-arm64")
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(i.Binary, "linux-arm64/chrome-linux-arm64/chrome") {
		t.Fatal("wrong runtime installed")
	}
	executable, e := elf.Open(i.Binary)
	if e != nil {
		t.Fatal(e)
	}
	defer executable.Close()
	if executable.Machine != elf.EM_AARCH64 || executable.Class != elf.ELFCLASS64 {
		t.Fatal("managed ARM runtime is not a 64-bit AArch64 executable")
	}
	// The second launch verifies the cache without needing the original archive.
	if _, e = EnsureFor(context.Background(), base, "/missing.zip", "linux-arm64"); e != nil {
		t.Fatal("verified cache not reused", e)
	}
	if _, e = EnsureFor(context.Background(), filepath.Join(t.TempDir(), "cache"), archive, "linux-amd64"); !errors.Is(e, ErrIntegrity) {
		t.Fatal("ARM64 archive accepted as an amd64 runtime", e)
	}
}
