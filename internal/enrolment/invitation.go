// Package enrolment encodes a single owner-issued setup invitation. The URI is
// a bearer secret and must be transferred over a trusted channel, never logged.
package enrolment

import (
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

var ErrInvitation = errors.New("INVALID_INVITATION")

type Invitation struct {
	Host        string
	Port        int
	Fingerprint string
	Code        string
}

func (i Invitation) Validate() error {
	if i.Host == "" || strings.ContainsAny(i.Host, " /\t\r\n?@#\\") || i.Port < 1 || i.Port > 65535 {
		return ErrInvitation
	}
	for _, s := range []string{i.Fingerprint, i.Code} {
		b, e := hex.DecodeString(s)
		if e != nil || len(b) != 32 {
			return ErrInvitation
		}
	}
	return nil
}
func (i Invitation) String() string {
	v := url.Values{"pin": {strings.ToLower(i.Fingerprint)}, "code": {strings.ToLower(i.Code)}}
	return (&url.URL{Scheme: "wolftunnel", Host: net.JoinHostPort(i.Host, strconv.Itoa(i.Port)), Path: "/enrol", RawQuery: v.Encode()}).String()
}
func Parse(raw string) (Invitation, error) {
	if len(raw) > 2048 {
		return Invitation{}, ErrInvitation
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "wolftunnel" || u.User != nil || u.Path != "/enrol" || u.Fragment != "" || u.Opaque != "" {
		return Invitation{}, ErrInvitation
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(values) != 2 || len(values["pin"]) != 1 || len(values["code"]) != 1 {
		return Invitation{}, ErrInvitation
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return Invitation{}, ErrInvitation
	}
	i := Invitation{Host: u.Hostname(), Port: port, Fingerprint: strings.ToLower(values.Get("pin")), Code: strings.ToLower(values.Get("code"))}
	if err = i.Validate(); err != nil {
		return Invitation{}, err
	}
	return i, nil
}
