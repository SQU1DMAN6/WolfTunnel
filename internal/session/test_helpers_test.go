package session_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"
)

func browserHash(t *testing.T, bin string) string {
	t.Helper()
	f, e := os.Open(bin)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		t.Fatal(e)
	}
	return hex.EncodeToString(h.Sum(nil))
}
