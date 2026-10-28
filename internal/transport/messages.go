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
	case "CONTROL_PROTOCOL_INVALID":
		return "The established browser control connection received an invalid protocol frame. The session closed safely."
	case "CONTROL_IDLE_TIMEOUT":
		return "The browser control connection stopped returning messages before its heartbeat deadline. The session ended; display connectivity is diagnosed separately."
	case "CONTROL_CONNECTION_FAILED":
		return "Browser control connection failed. Inspect its TLS, protocol and authentication cause in diagnostics."
	case "BROWSER_INITIALISATION_FAILED":
		return "Control authentication succeeded, but browser initialization did not complete. Check node browser startup logs using the correlation ID."
	case "DISPLAY_CONNECTION_FAILED":
		return "The browser was ready, but its separate display connection failed. Diagnostics show the underlying cause and the last control-channel observation."
	case "BROWSER_IDLE_TIMEOUT":
		return "The node closed this disposable browser after its configured browsing-idle limit. Reconnect to start a fresh session."
	case "DISPLAY_ATTACH_TIMEOUT":
		return "The node's ready browser did not receive a display attachment before its deadline. Check the separate pixel connection and approved proxy policy."
	case "FIRST_FRAME_TIMEOUT":
		return "The browser and display connection started, but no valid first image arrived before the deadline. Check node capture/stream logs."
	case "FRAME_IDLE_TIMEOUT":
		return "The attached display stopped delivering complete frames. The disposable browser session has ended."
	case "DISPLAY_STREAM_LOST":
		return "The separate display connection closed. This does not by itself establish that control connectivity failed."
	case "DISPLAY_FRAME_TRUNCATED":
		return "The display connection ended partway through a frame. The incomplete image was discarded and the session closed."
	case "DISPLAY_PROTOCOL_INVALID":
		return "The display connection received an invalid or truncated frame. The session ended safely."
	case "FRAME_DECODE_FAILED":
		return "A received image could not be decoded within the validated format and dimension rules. The session ended safely."
	case "RENDER_UPLOAD_FAILED", "RENDER_SUBMIT_FAILED", "RENDER_PRESENT_FAILED":
		return "The native display could not upload or present the received image. Check the client renderer/display and its log."
	case "POPUP_BLOCKED":
		return "A page requested a window without a supported user gesture. Use a link or Ctrl+click instead."

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
