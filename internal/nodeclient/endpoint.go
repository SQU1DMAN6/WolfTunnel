package nodeclient

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// NormalizeEndpoint accepts pasted host:port addresses without constructing an
// invalid nested host:port. Transport remains pinned TLS regardless of notation.
func NormalizeEndpoint(raw string, port int) (string, int, error) {
	host := strings.TrimSpace(raw)
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		u, e := url.Parse(host)
		if e != nil || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return "", 0, &Error{Code: "INVALID_CONFIGURATION"}
		}
		host = u.Host
	}
	if h, p, e := net.SplitHostPort(host); e == nil {
		var err error
		port, err = strconv.Atoi(p)
		if err != nil {
			return "", 0, &Error{Code: "INVALID_CONFIGURATION"}
		}
		host = h
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.Trim(host, "[]")
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return "", 0, &Error{Code: "INVALID_CONFIGURATION"}
	}
	if host == "" || strings.ContainsAny(host, "/ \t\r\n?@#\\") || port < 1 || port > 65535 {
		return "", 0, &Error{Code: "INVALID_CONFIGURATION"}
	}
	return host, port, nil
}
