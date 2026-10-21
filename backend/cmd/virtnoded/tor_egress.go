package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
)

// A runtime whose egress mode is "tor" gets its own bridge with a name the
// VM's firewall knows (vm-init.sh): everything arriving from a bridge in the
// tor_bridges set is redirected to Tor's TransPort and DNSPort, and nothing
// from it is forwarded, so traffic Tor cannot carry (UDP, IPv6) never leaves.
// The phone's viewer link never crosses the bridge and stays direct.
const (
	egressDirect      = "direct"
	egressTor         = "tor"
	torBridgePrefix   = "vrt-"
	torFirewallTable  = "virtroid"
	torFirewallSet    = "tor_bridges"
	torControlTimeout = 5 * time.Second
	// Docker's option naming a bridge network's Linux interface.
	dockerBridgeNameOption = "com.docker.network.bridge.name"
)

var errTorEgressUnavailable = errors.New("this node cannot route a runtime through Tor")

func runtimeEgressMode(runtime runtimeAssignment) string {
	if strings.EqualFold(strings.TrimSpace(runtime.EgressMode), egressTor) {
		return egressTor
	}
	return egressDirect
}

// torBridgeName is a Linux interface name (at most 15 bytes) unique to the
// runtime.
func torBridgeName(runtimeID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(runtimeID)))
	return torBridgePrefix + hex.EncodeToString(digest[:])[:11]
}

// runtimeBridgeName is the bridge a runtime's network must use: a Tor
// bridge, or "" for Docker's own br-* name.
func runtimeBridgeName(runtime runtimeAssignment) string {
	if runtimeEgressMode(runtime) == egressTor {
		return torBridgeName(runtime.ID)
	}
	return ""
}

func (n *nodeAgent) checkEgressSupported(runtime runtimeAssignment) error {
	if runtimeEgressMode(runtime) != egressTor {
		return nil
	}
	if !n.cfg.TorEgress || !runtimeUsesPerRuntimeNetwork() {
		return errTorEgressUnavailable
	}
	return nil
}

// allowTorBridge adds the runtime's bridge to the firewall's Tor set. It runs
// before the network exists, so no packet from it ever goes out directly.
func (n *nodeAgent) allowTorBridge(ctx context.Context, runtimeID string) error {
	return runNft(ctx, "add", "element", "inet", torFirewallTable, torFirewallSet, "{ \""+torBridgeName(runtimeID)+"\" }")
}

// forgetTorBridge removes the bridge from the Tor set once its network is
// gone. Leaving it would be harmless: the name belongs to this runtime only.
func (n *nodeAgent) forgetTorBridge(ctx context.Context, runtimeID string) error {
	if !n.cfg.TorEgress {
		return nil
	}
	err := runNft(ctx, "delete", "element", "inet", torFirewallTable, torFirewallSet, "{ \""+torBridgeName(runtimeID)+"\" }")
	if err != nil && strings.Contains(err.Error(), "No such file or directory") {
		return nil
	}
	return err
}

func runNft(ctx context.Context, args ...string) error {
	output, err := exec.CommandContext(ctx, "nft", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

// newTorIdentity asks Tor for new circuits, so a runtime started again with
// a new persona does not reuse the exit it had before. The control socket
// is reachable only by root on the VM, so it needs no password.
func (n *nodeAgent) newTorIdentity(ctx context.Context) error {
	dialer := net.Dialer{Timeout: torControlTimeout}
	conn, err := dialer.DialContext(ctx, "unix", n.cfg.TorControlSocket)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(torControlTimeout))
	if _, err := conn.Write([]byte("AUTHENTICATE\r\nSIGNAL NEWNYM\r\nQUIT\r\n")); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	for range 2 {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if !strings.HasPrefix(line, "250") {
			return fmt.Errorf("tor control: %s", strings.TrimSpace(line))
		}
	}
	return nil
}
