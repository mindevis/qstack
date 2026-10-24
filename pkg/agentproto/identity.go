// Package agentproto implements the protocol between service controllers
// and host agents (spec 02 §10, ADR-0005): the controller keeps every host's
// desired state in a Hub and serves it with HostAgentService; the agent's
// Client watches it (snapshot, then deltas with revisions), caches it on
// disk, applies it level-triggered and reports status.
//
// Hosts authenticate with mTLS (TLS 1.3, client certificate required) and
// are identified by the SAN URI spiffe://qstack/<region>/host/<hst-id>; a
// host only ever receives its own desired state (REQ-PLT-100).
package agentproto

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/ids"
)

// HostURI is the SPIFFE ID of a host certificate.
func HostURI(region, hostID string) *url.URL {
	return &url.URL{Scheme: "spiffe", Host: "qstack", Path: "/" + region + "/host/" + hostID}
}

// ParseHostURI returns the region and host ID of a host SPIFFE ID.
func ParseHostURI(u *url.URL) (region, hostID string, err error) {
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if u.Scheme != "spiffe" || u.Host != "qstack" || len(parts) != 3 || parts[1] != "host" || parts[0] == "" {
		return "", "", fmt.Errorf("agentproto: %q is not a host SPIFFE ID", u.String())
	}
	if err := ids.ParseKind(parts[2], ids.Host); err != nil {
		return "", "", fmt.Errorf("agentproto: host id: %w", err)
	}
	return parts[0], parts[2], nil
}

// hostFromPeer returns the host ID of the verified client certificate,
// which must belong to region.
func hostFromPeer(ctx context.Context, region string) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", errors.New("no peer")
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.VerifiedChains) == 0 || len(info.State.VerifiedChains[0]) == 0 {
		return "", errors.New("no verified client certificate")
	}
	leaf := info.State.VerifiedChains[0][0]
	var host string
	for _, u := range leaf.URIs {
		r, h, err := ParseHostURI(u)
		if err != nil {
			continue
		}
		if r != region {
			return "", fmt.Errorf("certificate of region %q", r)
		}
		if host != "" && host != h {
			return "", errors.New("certificate names several hosts")
		}
		host = h
	}
	if host == "" {
		return "", errors.New("certificate has no host SPIFFE ID")
	}
	return host, nil
}

// HostFromContext authenticates the calling host (server side).
func HostFromContext(ctx context.Context, region string) (string, error) {
	host, err := hostFromPeer(ctx, region)
	if err != nil {
		return "", apierr.Unauthenticated()
	}
	return host, nil
}

// ServerTLS returns the controller's TLS config: TLS 1.3 only and a client
// certificate signed by hostCAs is required.
func ServerTLS(cert tls.Certificate, hostCAs *x509.CertPool) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    hostCAs,
	}
}

// ClientTLS returns the agent's TLS config: its host certificate and the
// control plane CA (pinned at join).
func ClientTLS(hostCert tls.Certificate, serverCAs *x509.CertPool, serverName string) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{hostCert},
		RootCAs:      serverCAs,
		ServerName:   serverName,
	}
}
