package nodeclient

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
	"wolftunnel/internal/audit"
	"wolftunnel/internal/browserdiag"
)

// FailureDetail reports safe useful causes; it never copies untrusted peer text,
// URLs, typed input, invitation strings or arbitrary syscall/path arguments.
func FailureDetail(err error) string {
	if err == nil {
		return ""
	}
	if advice := browserdiag.Message(ErrorCode(err)); advice != "" {
		return advice
	}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "Connection refused: the address was reached but no node accepted this port."
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "No network route to the configured node address."
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return "Permission denied by the operating system or state-directory permissions."
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "DNS could not resolve the configured node hostname."
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "The node connection or operation timed out."
	}
	switch ErrorCode(err) {
	case "CLIENT_KEY_MISSING":
		return "The enrolled installation key is missing. Restore its protected backup; a code cannot recover a lost private key."
	case "CLIENT_IDENTITY_MISMATCH":
		return "The saved configuration and installation key belong to different identities."
	case "INVALID_FINGERPRINT":
		return "The certificate pin must contain exactly 64 hexadecimal characters."
	case "NODE_IDENTITY_UNVERIFIED":
		return "A new connection requires the owner's verified certificate pin or complete invitation."
	case "NODE_IDENTITY_CHANGED":
		return "The received certificate differs from the saved trusted pin; connection blocked."
	case "NODE_IDENTITY_EXPIRED":
		return "The node certificate is not currently valid; check clocks and certificate expiry."
	case "ENROLMENT_CODE_INVALID":
		return "The enrolment code is malformed, consumed, or issued by a different node/state directory."
	case "ENROLMENT_CODE_REQUIRED":
		return "Enter a complete invitation or a fresh code for the configured verified node."
	case "AUTH_FAILED":
		return "The installation identity is not authorised, expired, or revoked on this node."
	case "INVALID_INVITATION":
		return "Paste the entire wolftunnel:// invitation; a bare code needs address and an owner-verified pin."
	default:
		return fmt.Sprintf("Operation failed (%s; error type %T).", ErrorCode(err), err)
	}
}
func ReportTo(logger *audit.Logger, event string, err error, endpoint string) {
	if logger == nil {
		return
	}
	logger.Log(audit.Entry{Component: "client", Event: event, Code: ErrorCode(err), Error: FailureDetail(err), Endpoint: endpoint})
}
func (c Client) Report(event string, err error) {
	endpoint := ""
	if c.Config.Validate() == nil {
		endpoint = c.Config.Address()
	}
	ReportTo(c.Log, event, err, endpoint)
}

// Logged operations retain the original error, including its typed network cause.
func (c Client) reportOperation(event string, start time.Time, err error) {
	if err != nil {
		c.Report(event, err)
	} else {
		c.trace(event, "", "ok", start)
	}
}
