package audit

import "testing"

func TestSanitizeURL(t *testing.T) {
	got := SanitizeURL("https://user:password@example.com/path?token=secret&x=1#fragment")
	want := "https://example.com/path"
	if got != want {
		t.Fatalf("SanitizeURL() = %q, want %q", got, want)
	}
}
