package browserdiag

import "testing"

func TestCatalogHasActionableAdviceAndRejectsPeerText(t *testing.T) {
	for code, a := range catalog {
		if a.Summary == "" || a.Remedy == "" || len(Message(code)) < 50 {
			t.Fatal("missing advice", code)
		}
	}
	if Message("secret-token") != "" {
		t.Fatal("arbitrary peer text accepted")
	}
}
