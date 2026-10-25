package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"wolftunnel/internal/audit"
	"wolftunnel/internal/browser"
	"wolftunnel/internal/enrolment"
	"wolftunnel/internal/node"
	"wolftunnel/internal/storage"
)

func main() {
	root, err := storage.DefaultDir(".wolftunnel")
	if err != nil {
		log.Fatal(err)
	}
	state := flag.String("state-dir", root, "private node state directory")
	listen := flag.String("listen", ":13300", "TCP listener")
	issue := flag.Bool("issue-enrolment", false, "create a single-use 30-day code and exit; stdout is secret")
	inviteAddress := flag.String("invite-address", "", "with -issue-enrolment, output one setup invitation for host:port (secret)")
	revoke := flag.String("revoke", "", "revoke a client ID and exit")
	browserConfig := flag.String("browser-config", "", "private browser JSON configuration")
	browserBin := flag.String("browser-bin", "", "pinned Chromium/Chromium-based executable")
	browserHash := flag.String("browser-sha256", "", "expected browser executable SHA-256")
	flag.Parse()
	*state, err = filepath.Abs(*state)
	if err != nil {
		log.Fatal(err)
	}
	if *issue && *revoke != "" {
		log.Fatal("choose one administrative operation")
	}
	s, err := node.Open(*state)
	if err != nil {
		log.Fatal(err)
	}
	if *issue {
		var invitation enrolment.Invitation
		if *inviteAddress != "" {
			host, port, err := net.SplitHostPort(*inviteAddress)
			if err != nil {
				log.Fatal("invite address must be host:port")
			}
			invitation.Host = host
			invitation.Port, err = strconv.Atoi(port)
			invitation.Fingerprint = s.Fingerprint
			invitation.Code = strings.Repeat("0", 64)
			if err != nil || invitation.Validate() != nil {
				log.Fatal("invalid invitation address")
			}
		}
		code, err := s.Auth.IssueCode()
		if err != nil {
			log.Fatal(err)
		}
		s.Audit.Log(audit.Entry{Event: "enrolment_code_issued"})
		if *inviteAddress != "" {
			invitation.Code = code
			fmt.Println(invitation.String())
		} else {
			fmt.Println(code)
		}
		return
	}
	if *revoke != "" {
		if err = s.Auth.Revoke(*revoke); err != nil {
			log.Fatal(err)
		}
		s.Audit.Log(audit.Entry{Event: "client_revoked", ClientID: *revoke})
		fmt.Println("Client revoked")
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	bc := browser.DefaultConfig()
	if *browserConfig != "" {
		if err = storage.Read(*browserConfig, &bc); err != nil {
			log.Fatal(err)
		}
	}
	if *browserBin != "" {
		bc.Binary = *browserBin
		bc.SHA256 = *browserHash
	}
	if err = s.EnableBrowser(bc); err != nil {
		log.Fatal(err)
	}
	defer stop()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("WolfTunnel v0.5 node ready on %s\nState: %s\nCertificate SHA-256: %s\nEnrolment requires an owner-issued code. Browser sessions use protocol v3.\n", ln.Addr(), *state, s.Fingerprint)
	if err = s.Serve(ctx, ln); err != nil {
		log.Fatal(err)
	}
}
