package transport

func StateMessage(s State) string {
	switch s {
	case Resolving:
		return "Resolving node or configured proxy…"
	case Connecting:
		return "Connecting…"
	case ProxyConnect:
		return "Requesting the configured proxy tunnel…"
	case TLSHandshake:
		return "Negotiating TLS…"
	case VerifyingIdentity:
		return "Verifying the pinned node identity…"
	case Negotiating:
		return "Negotiating the browser protocol…"
	case Authenticating:
		return "Authenticating this installation…"
	case CreatingSession, InitialisingBrowser:
		return "Starting the isolated browser…"
	case ConnectingPixels:
		return "Connecting the display stream…"
	case RetryWait:
		return "Connection interrupted; waiting to retry…"
	case Connected:
		return "Connection ready"
	case Failed:
		return "Connection failed"
	case Cancelled:
		return "Connection cancelled"
	default:
		return "Disconnected"
	}
}
func FailureMessage(code string) string {
	switch code {
	case "UNSUPPORTED_TRANSPORT":
		return "Unsupported transport. Select tls-tcp or explicit http-connect."
	case "INVALID_PROXY":
		return "Enter a valid HTTP/HTTPS proxy URL without credentials, path or query, and select http-connect."
	case "CONNECTION_REFUSED":
		return "The endpoint refused the TCP connection. Check the node/proxy address and listener."
	case "CONNECTION_TIMEOUT":
		return "TCP connection timed out. Endpoint or network filtering may be involved; the cause is unconfirmed."
	case "CONNECTION_INTERRUPTED":
		return "The connection closed or reset. Check endpoint availability and network policy."
	case "TLS_HANDSHAKE_FAILED":
		return "TLS negotiation failed or timed out. Check TLS support, inspection policy and endpoint availability."
	case "NODE_DNS_ERROR":
		return "Could not resolve the configured node or proxy hostname."
	case "PROXY_UNAVAILABLE":
		return "The explicitly configured proxy is unavailable; no direct fallback was attempted."
	case "PROXY_REJECTED":
		return "The configured proxy rejected CONNECT to this node. Ask its administrator to approve the endpoint and port."
	case "PROXY_AUTH_REQUIRED":
		return "The proxy requires authentication. Interactive, NTLM and Kerberos proxy authentication are not supported."
	case "PROXY_TLS_FAILED":
		return "The HTTPS proxy certificate or TLS handshake failed. Verify the approved proxy hostname and system trust roots."
	case "PROXY_RESPONSE_INVALID":
		return "The proxy returned an invalid, oversized or incomplete CONNECT response."
	case "PIXEL_STREAM_FAILED":
		return "The display stream stopped. This browser session ended; reconnecting starts fresh website state."
	case "CONTROL_STREAM_FAILED":
		return "The browser control stream stopped. This session ended; reconnecting starts fresh website state."
	case "UNSUPPORTED_INPUT":
		return "This node does not support the requested gesture. Use scrolling or Ctrl+plus/minus, or update the node."
	}
	return ""
}
