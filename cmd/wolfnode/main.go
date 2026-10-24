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
	issue := flag.Bool("issue-enrolment", false, "create a single-use ten-minute code and exit; stdout is secret")
	inviteAddress := flag.String("invite-address", "", "with -issue-enrolment, output one setup invitation for host:port (secret)")
	revoke := flag.String("revoke", "", "revoke a client ID and exit")
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
	defer stop()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("WolfTunnel v0.2 node ready on %s\nState: %s\nCertificate SHA-256: %s\nEnrolment requires an owner-issued code.\n", ln.Addr(), *state, s.Fingerprint)
	if err = s.Serve(ctx, ln); err != nil {
		log.Fatal(err)
	}
}
