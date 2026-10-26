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

func TestNewLocalCategoriesKeepExistingClientCompatibility(t *testing.T) {
	for local, want := range map[string]string{"BROWSER_APPARMOR_SETUP_REQUIRED": "BROWSER_NAMESPACE_DENIED", "BROWSER_LAUNCHER_UNSAFE": "BROWSER_BINARY_INVALID", "BROWSER_TASK_LIMIT": "BROWSER_TASK_LIMIT"} {
		if got := PeerCode(local); got != want {
			t.Fatal(local, got, want)
		}
	}
}
