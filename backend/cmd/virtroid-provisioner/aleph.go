package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// alephInstance is one instance owned by the provisioner's Aleph account.
type alephInstance struct {
	ItemHash string
	Name     string
	NodeHash string
	IPv6     string
}

type createSpec struct {
	Name       string
	CRNHash    string
	Runtime    string
	Image      string
	VCPUs      int
	MemoryMiB  int
	DiskMiB    int
	AMDProduct string
}

type unlockSpec struct {
	ItemHash       string
	CRNHash        string
	AMDProduct     string
	PassphraseFile string
	Secrets        map[string]string
}

// alephDriver creates, unlocks, lists and deletes confidential instances.
type alephDriver interface {
	List(ctx context.Context) ([]alephInstance, error)
	Create(ctx context.Context, spec createSpec) (string, error)
	Unlock(ctx context.Context, spec unlockSpec) error
	Delete(ctx context.Context, itemHash string) error
}

// errAttestation marks an unlock that failed because the VM did not prove
// it is the expected confidential VM. Such a VM is deleted, never retried.
var errAttestation = errors.New("attestation failed")

// cliDriver drives Aleph's official `aleph` CLI, which builds and signs the
// INSTANCE message, computes the SEV-SNP launch measurements from the runtime
// bundle, and verifies the guest over attested TLS before sending secrets.
type cliDriver struct {
	bin     string
	account string
	env     []string
	timeout time.Duration
}

// run invokes the CLI. identityFlag names the flag that selects the
// provisioner's account on this command: signing commands take --account,
// `instance list` takes --address.
func (d cliDriver) run(ctx context.Context, identityFlag string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	full := append([]string{"--json"}, args...)
	if d.account != "" {
		full = append(full, identityFlag, d.account)
	}
	cmd := exec.CommandContext(ctx, d.bin, full...)
	cmd.Env = d.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("aleph %s: %w: %s", args[0]+" "+args[1], err, lastLines(stderr.String(), 6))
	}
	return stdout.Bytes(), nil
}

func (d cliDriver) List(ctx context.Context) ([]alephInstance, error) {
	out, err := d.run(ctx, "--address", "instance", "list")
	if err != nil {
		return nil, err
	}
	return parseInstanceList(out)
}

func (d cliDriver) Create(ctx context.Context, spec createSpec) (string, error) {
	out, err := d.run(ctx, "--account", "instance", "create", spec.Name,
		"--confidential", "--tee", "sev-snp",
		"--runtime", spec.Runtime,
		"--image", spec.Image,
		"--vcpus", strconv.Itoa(spec.VCPUs),
		"--memory", strconv.Itoa(spec.MemoryMiB)+"MiB",
		"--disk-size", strconv.Itoa(spec.DiskMiB)+"MiB",
		"--crn", spec.CRNHash,
	)
	if err != nil {
		return "", err
	}
	return parseCreateOutput(out)
}

func (d cliDriver) Unlock(ctx context.Context, spec unlockSpec) error {
	args := []string{"instance", "unlock", spec.ItemHash,
		"--crn", spec.CRNHash,
		"--amd-product", spec.AMDProduct,
		"--passphrase-file", spec.PassphraseFile,
	}
	keys := make([]string, 0, len(spec.Secrets))
	for key := range spec.Secrets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--secret", key+"="+spec.Secrets[key])
	}
	_, err := d.run(ctx, "--account", args...)
	if err != nil && isAttestationFailure(err.Error()) {
		return fmt.Errorf("%w: %v", errAttestation, err)
	}
	return err
}

func (d cliDriver) Delete(ctx context.Context, itemHash string) error {
	_, err := d.run(ctx, "--account", "instance", "delete", itemHash, "--yes", "--reason", "Virtroid runtime VM retired")
	return err
}

// parseCreateOutput reads the item hash from `aleph --json instance create`.
func parseCreateOutput(out []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(out))
	for {
		var doc struct {
			ItemHash string `json:"item_hash"`
		}
		err := decoder.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return "", errors.New("aleph instance create printed no item_hash")
		}
		if err != nil {
			return "", fmt.Errorf("parse aleph instance create output: %w", err)
		}
		if doc.ItemHash != "" {
			return strings.ToLower(doc.ItemHash), nil
		}
	}
}

func parseInstanceList(out []byte) ([]alephInstance, error) {
	var rows []struct {
		ItemHash string  `json:"item_hash"`
		Name     *string `json:"name"`
		NodeHash *string `json:"node_hash"`
		IPv6     *string `json:"ipv6"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse aleph instance list output: %w", err)
	}
	instances := make([]alephInstance, 0, len(rows))
	for _, row := range rows {
		instance := alephInstance{ItemHash: strings.ToLower(row.ItemHash)}
		if row.Name != nil {
			instance.Name = *row.Name
		}
		if row.NodeHash != nil {
			instance.NodeHash = strings.ToLower(*row.NodeHash)
		}
		if row.IPv6 != nil {
			instance.IPv6 = vmAddress(*row.IPv6)
		}
		instances = append(instances, instance)
	}
	return instances, nil
}

// vmAddress returns the VM's own IPv6 address, or "" while the CRN only
// reports the VM's subnet.
func vmAddress(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "/") {
		return ""
	}
	ip := net.ParseIP(raw)
	if ip == nil || !ip.IsGlobalUnicast() {
		return ""
	}
	return ip.String()
}

// isAttestationFailure separates "the VM proved something wrong" from "the
// VM or AMD's key service is not reachable yet". Unknown errors count as
// transient: the CLI sends no secret unless verification passed, and a VM
// that never unlocks is retired by the control plane's setup timeout.
func isAttestationFailure(message string) bool {
	message = strings.ToLower(message)
	for _, transient := range []string{
		"connection", "timed out", "timeout", "dns", "unreachable", "failed to fetch",
		"kds", "503", "502", "504", "not reachable", "pending", "no networking", "try again",
	} {
		if strings.Contains(message, transient) {
			return false
		}
	}
	for _, fatal := range []string{
		"measurement", "verification failed", "policy", "tcb", "vmpl", "signature",
		"ark certificate", "debug", "not an snp", "attestation report",
	} {
		if strings.Contains(message, fatal) {
			return true
		}
	}
	return false
}

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
