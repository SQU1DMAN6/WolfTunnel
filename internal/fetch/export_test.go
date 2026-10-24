package fetch

import (
	"context"
	"net"
)

// ControlledFetcherForTest is available only in test builds. Production has no
// option to bypass address validation or redirect policy.
func ControlledFetcherForTest(address string) *Fetcher {
	f := New(DefaultConfig())
	f.resolver = resolverFunc(publicDNS)
	f.dial = func(ctx context.Context, network, target string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	return f
}
