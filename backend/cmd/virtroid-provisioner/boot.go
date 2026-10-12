package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"time"
)

// bootReader tells one boot of a VM from the next. The VM's attestation agent
// makes a new TLS key every time the VM starts, so the key identifies the boot.
type bootReader interface {
	BootIdentity(ctx context.Context, itemHash, ipv6 string) (string, error)
}

// agentBoots reads the agent's key through the compute node's port mapping,
// which aleph's own unlock uses, or else on the VM's IPv6 address.
type agentBoots struct {
	nodes *alephProgress
}

func (b agentBoots) BootIdentity(ctx context.Context, itemHash, ipv6 string) (string, error) {
	addr := b.nodes.agentAddress(ctx, itemHash)
	if addr == "" {
		if net.ParseIP(ipv6) == nil {
			return "", errors.New("the VM has no known agent address")
		}
		addr = net.JoinHostPort(ipv6, "8443")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	dialer := tls.Dialer{Config: &tls.Config{
		// Only the key is read, to tell boots apart. Trusting the VM is the
		// attested unlock's job.
		InsecureSkipVerify: true, //nolint:gosec
		MinVersion:         tls.VersionTLS12,
	}}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("the agent presented no certificate")
	}
	sum := sha256.Sum256(certs[0].RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:]), nil
}

// agentAddress is where the compute node forwards to the VM's agent (8443),
// or "" when it does not say.
func (p *alephProgress) agentAddress(ctx context.Context, itemHash string) string {
	execution, ok := p.execution(ctx, itemHash)
	if !ok {
		return ""
	}
	port, ok := execution.Networking.MappedPorts["8443"]
	ip := net.ParseIP(execution.Networking.HostIPv4)
	if !ok || port.Host <= 0 || port.Host > 65535 || ip == nil || ip.To4() == nil {
		return ""
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port.Host))
}
