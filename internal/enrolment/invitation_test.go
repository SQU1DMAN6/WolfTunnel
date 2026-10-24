package enrolment

import (
	"strings"
	"testing"
)

func TestInvitationRoundTripAndValidation(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "node.example", "::1"} {
		want := Invitation{Host: host, Port: 13300, Fingerprint: strings.Repeat("a", 64), Code: strings.Repeat("b", 64)}
		got, err := Parse(want.String())
		if err != nil || got != want {
			t.Fatal("invitation did not round trip")
		}
	}
	valid := Invitation{Host: "node.example", Port: 13300, Fingerprint: strings.Repeat("a", 64), Code: strings.Repeat("b", 64)}.String()
	for _, raw := range []string{"", "https://example.com", valid + "&code=x", valid + "&other=x", strings.Replace(valid, "/enrol", "/other", 1), valid + "#fragment", strings.Replace(valid, "node.example", "user:password@node.example", 1), strings.Replace(valid, "13300", "0", 1), strings.Repeat("x", 2049)} {
		if _, err := Parse(raw); err == nil {
			t.Fatal("invalid invitation accepted")
		}
	}
}
func FuzzInvitation(f *testing.F) {
	f.Add(Invitation{Host: "127.0.0.1", Port: 13300, Fingerprint: strings.Repeat("a", 64), Code: strings.Repeat("b", 64)}.String())
	f.Add("wolftunnel://invalid/enrol")
	f.Fuzz(func(t *testing.T, s string) {
		if i, e := Parse(s); e == nil {
			again, e := Parse(i.String())
			if e != nil || again != i {
				t.Fatal("accepted invitation is not canonical")
			}
		}
	})
}
