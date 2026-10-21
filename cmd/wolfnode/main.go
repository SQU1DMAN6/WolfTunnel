package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"time"

	"wolftunnel/internal/audit"
	"wolftunnel/internal/protocol"
)

func main() {
	listen := flag.String("listen", ":13300", "TCP listen address")
	certFile := flag.String("cert", "wolfnode.crt", "TLS certificate PEM")
	keyFile := flag.String("key", "wolfnode.key", "TLS private key PEM")
	logDir := flag.String("log-dir", "./logs", "security audit log directory")
	retention := flag.Duration("retention", 7*24*time.Hour, "maximum audit log retention")
	token := flag.String("token", os.Getenv("WOLFTUNNEL_TOKEN"), "shared client authentication token (prefer WOLFTUNNEL_TOKEN environment variable)")
	flag.Parse()
	if len(*token) < 32 {
		log.Fatal("authentication token must be at least 32 characters; set WOLFTUNNEL_TOKEN")
	}
	pair, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		log.Fatalf("load TLS certificate/key: %v", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		log.Fatalf("parse TLS certificate: %v", err)
	}
	fmt.Printf("Wolf Node listening on %s\nTLS certificate SHA-256 pin: %X\n", *listen, sha256Sum(cert.Raw))
	fmt.Println("Share the certificate pin with the client through a trusted channel. Never share the private key.")
	logger, err := audit.New(*logDir, *retention)
	if err != nil {
		log.Fatalf("initialise audit logger: %v", err)
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			logger.Purge()
		}
	}()
	cfg := &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	ln, err := tls.Listen("tcp", *listen, cfg)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept failed: %v", err)
			continue
		}
		go handle(conn, *token, logger)
	}
}

func sha256Sum(b []byte) [32]byte { return sha256.Sum256(b) }

func handle(raw net.Conn, token string, logger *audit.Logger) {
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(15 * time.Second))
	conn, ok := raw.(*tls.Conn)
	if !ok {
		return
	}
	if err := conn.Handshake(); err != nil {
		return
	}
	m, err := protocol.Read(conn)
	if err != nil {
		return
	}
	switch m.Type {
	case protocol.TypePing:
		var p protocol.Ping
		if json.Unmarshal(m.Payload, &p) != nil || !validToken(p.Token, token) {
			_ = writeResponse(conn, protocol.TypePong, protocol.Response{Status: "unauthorized", State: "Client authentication failed", CanRespond: false})
			return
		}
		_ = writeResponse(conn, protocol.TypePong, protocol.Response{Status: "ok", State: "Wolf Node reachable; authenticated response channel available", CanRespond: true})
	case protocol.TypeRequest:
		var req protocol.Request
		if json.Unmarshal(m.Payload, &req) != nil || !validToken(req.Token, token) {
			_ = writeResponse(conn, protocol.TypeAck, protocol.Response{Status: "unauthorized", State: "Client authentication failed", CanRespond: false})
			return
		}
		target, err := validateTarget(req.Target)
		if err != nil {
			logger.Log(audit.Entry{Event: "request_rejected", ID: req.RequestID, Status: "invalid_target"})
			_ = writeResponse(conn, protocol.TypeAck, protocol.Response{RequestID: req.RequestID, Status: "rejected", State: "Only absolute http/https URLs are accepted in v0", CanRespond: false})
			return
		}
		logger.Log(audit.Entry{Event: "dummy_request_received", ID: req.RequestID, Target: audit.SanitizeURL(target), Status: "acknowledged"})
		_ = writeResponse(conn, protocol.TypeAck, protocol.Response{RequestID: req.RequestID, Status: "ok", State: "Wolf Node received the request. v0 does not fetch the destination; an authenticated response channel is available.", CanRespond: true})
	default:
		logger.Log(audit.Entry{Event: "protocol_error", Status: "unknown_message_type"})
		_ = writeResponse(conn, protocol.TypeAck, protocol.Response{Status: "rejected", State: "Unknown message type", CanRespond: false})
	}
}

func validToken(got, want string) bool {
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func validateTarget(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", fmt.Errorf("invalid target")
	}
	return u.String(), nil
}

func writeResponse(conn net.Conn, typ byte, r protocol.Response) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return protocol.Write(conn, protocol.Message{Type: typ, Payload: b})
}
