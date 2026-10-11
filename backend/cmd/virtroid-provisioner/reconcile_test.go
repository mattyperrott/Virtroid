package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"virtroid/backend/internal/nodeauth"
)

type fakeControlPlane struct {
	vms     []provisionedVM
	reports []struct {
		vmID   string
		report vmReport
	}
}

func (f *fakeControlPlane) Work(context.Context) ([]provisionedVM, error) { return f.vms, nil }

func (f *fakeControlPlane) Report(_ context.Context, vmID string, report vmReport) error {
	f.reports = append(f.reports, struct {
		vmID   string
		report vmReport
	}{vmID, report})
	return nil
}

func (f *fakeControlPlane) last(t *testing.T) vmReport {
	t.Helper()
	if len(f.reports) == 0 {
		t.Fatal("no report was sent")
	}
	return f.reports[len(f.reports)-1].report
}

type fakeAleph struct {
	instances []alephInstance
	created   []createSpec
	unlocked  []unlockSpec
	deleted   []string
	unlockErr error
}

func (f *fakeAleph) List(context.Context) ([]alephInstance, error) { return f.instances, nil }

func (f *fakeAleph) Create(_ context.Context, spec createSpec) (string, error) {
	f.created = append(f.created, spec)
	return strings.Repeat("e", 64), nil
}

func (f *fakeAleph) Unlock(_ context.Context, spec unlockSpec) error {
	f.unlocked = append(f.unlocked, spec)
	return f.unlockErr
}

func (f *fakeAleph) Delete(_ context.Context, itemHash string) error {
	f.deleted = append(f.deleted, itemHash)
	return nil
}

var (
	crnA = strings.Repeat("a", 64)
	crnB = strings.Repeat("b", 64)
	hash = strings.Repeat("c", 64)
)

func testReconciler(t *testing.T, cp *fakeControlPlane, aleph *fakeAleph) *reconciler {
	t.Helper()
	return newReconciler(provisionerConfig{
		VMControlPlaneURL: "https://virtroid.example",
		StateDir:          t.TempDir(),
		Runtime:           strings.Repeat("1", 64),
		Image:             strings.Repeat("2", 64),
		VCPUs:             2,
		MemoryMiB:         4096,
		DiskMiB:           20480,
		PassphraseFile:    "/run/secrets/luks",
		RegionCRNs: map[string][]crnTarget{
			"snp-eu": {{Hash: crnA, AMDProduct: "Genoa"}, {Hash: crnB, AMDProduct: "Milan"}},
		},
	}, cp, aleph)
}

func TestRequestedVMIsCreatedOnTheLeastLoadedNode(t *testing.T) {
	cp := &fakeControlPlane{vms: []provisionedVM{{ID: "pvm-1", Region: "snp-eu", State: vmStateRequested, Desired: "present"}}}
	aleph := &fakeAleph{instances: []alephInstance{{ItemHash: strings.Repeat("d", 64), Name: "someone-else", NodeHash: crnA}}}
	r := testReconciler(t, cp, aleph)
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(aleph.created) != 1 {
		t.Fatalf("created %d instances, want 1", len(aleph.created))
	}
	spec := aleph.created[0]
	if spec.Name != "virtroid-pvm-1" || spec.CRNHash != crnB || spec.Runtime != r.cfg.Runtime || spec.Image != r.cfg.Image {
		t.Fatalf("create spec = %+v", spec)
	}
	if got := cp.last(t); got.State != vmStateCreating || got.ItemHash != strings.Repeat("e", 64) || got.CRNHash != crnB {
		t.Fatalf("report = %+v", got)
	}

	// The report did not land and the listing lags: no second instance.
	cp.reports = nil
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(aleph.created) != 1 || cp.last(t).State != vmStateCreating {
		t.Fatalf("second pass created=%d report=%+v", len(aleph.created), cp.last(t))
	}
}

func TestRequestedVMAdoptsAnInstanceCreatedBeforeARestart(t *testing.T) {
	cp := &fakeControlPlane{vms: []provisionedVM{{ID: "pvm-1", Region: "snp-eu", State: vmStateRequested, Desired: "present"}}}
	aleph := &fakeAleph{instances: []alephInstance{{ItemHash: hash, Name: "virtroid-pvm-1", NodeHash: crnA}}}
	if err := testReconciler(t, cp, aleph).runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(aleph.created) != 0 {
		t.Fatal("created a second instance for the same VM")
	}
	if got := cp.last(t); got.State != vmStateCreating || got.ItemHash != hash {
		t.Fatalf("report = %+v", got)
	}
}

func TestRegionWithoutNodesFailsTheVM(t *testing.T) {
	cp := &fakeControlPlane{vms: []provisionedVM{{ID: "pvm-1", Region: "snp-us", State: vmStateRequested, Desired: "present"}}}
	aleph := &fakeAleph{}
	if err := testReconciler(t, cp, aleph).runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := cp.last(t); got.Failure == "" || len(aleph.created) != 0 {
		t.Fatalf("report = %+v created=%d", got, len(aleph.created))
	}
}

func TestCreatingVMWaitsForItsAddressThenGetsANodeKey(t *testing.T) {
	cp := &fakeControlPlane{vms: []provisionedVM{{ID: "pvm-1", Region: "snp-eu", State: vmStateCreating, Desired: "present", ItemHash: hash, CRNHash: crnA}}}
	aleph := &fakeAleph{instances: []alephInstance{{ItemHash: hash, Name: "virtroid-pvm-1", NodeHash: crnA}}}
	r := testReconciler(t, cp, aleph)
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cp.reports) != 0 {
		t.Fatalf("reported before the VM had an address: %+v", cp.reports)
	}

	aleph.instances[0].IPv6 = "2001:db8:3100:1781::10"
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := cp.last(t)
	if got.State != vmStateUnlocking || got.PublicIP != "2001:db8:3100:1781::10" {
		t.Fatalf("report = %+v", got)
	}
	if _, _, err := nodeauth.NormalizePublicKey(got.NodePublicKey); err != nil {
		t.Fatalf("reported node key is not a P-256 public key: %v", err)
	}
	info, err := os.Stat(r.nodeKeyPath("pvm-1"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("node key file mode = %v err=%v, want 0600", info.Mode().Perm(), err)
	}
	if _, again, _ := r.nodeKey("pvm-1"); again != got.NodePublicKey {
		t.Fatal("the node key changed between calls")
	}
}

func TestUnlockingVMReceivesItsSecretsOnlyThroughTheCLI(t *testing.T) {
	cp := &fakeControlPlane{vms: []provisionedVM{{ID: "pvm-1", Region: "snp-eu", State: vmStateUnlocking, Desired: "present", ItemHash: hash, CRNHash: crnB}}}
	aleph := &fakeAleph{unlockErr: errors.New("connection refused")}
	r := testReconciler(t, cp, aleph)
	clock := time.Unix(1_700_000_000, 0)
	r.now = func() time.Time { return clock }

	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(aleph.unlocked) != 1 || len(cp.reports) != 0 {
		t.Fatalf("transient failure: unlocks=%d reports=%+v", len(aleph.unlocked), cp.reports)
	}
	spec := aleph.unlocked[0]
	privateKey, _, _ := r.nodeKey("pvm-1")
	if spec.AMDProduct != "Milan" || spec.PassphraseFile != "/run/secrets/luks" ||
		spec.Secrets["virtroid_node_id"] != "pvm-1" || spec.Secrets["virtroid_node_key"] != privateKey ||
		spec.Secrets["virtroid_region"] != "snp-eu" || spec.Secrets["virtroid_control_plane"] != "https://virtroid.example" {
		t.Fatalf("unlock spec = %+v", spec)
	}

	// Retries are spaced out.
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(aleph.unlocked) != 1 {
		t.Fatal("retried the unlock immediately")
	}

	clock = clock.Add(unlockRetryEvery)
	aleph.unlockErr = nil
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := cp.last(t); got.State != vmStateBooting {
		t.Fatalf("report = %+v, want booting", got)
	}
}

func TestFailedAttestationRetiresTheVM(t *testing.T) {
	cp := &fakeControlPlane{vms: []provisionedVM{{ID: "pvm-1", Region: "snp-eu", State: vmStateUnlocking, Desired: "present", ItemHash: hash, CRNHash: crnA}}}
	aleph := &fakeAleph{unlockErr: fmt.Errorf("%w: launch measurement does not match", errAttestation)}
	if err := testReconciler(t, cp, aleph).runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := cp.last(t); got.Failure == "" || got.State != "" {
		t.Fatalf("report = %+v, want a failure", got)
	}
}

func TestRetiredVMIsDeletedAndItsKeyRemoved(t *testing.T) {
	cp := &fakeControlPlane{vms: []provisionedVM{{ID: "pvm-1", Region: "snp-eu", State: "ready", Desired: "absent", ItemHash: hash, CRNHash: crnA}}}
	aleph := &fakeAleph{instances: []alephInstance{{ItemHash: hash, Name: "virtroid-pvm-1", NodeHash: crnA}}}
	r := testReconciler(t, cp, aleph)
	if _, _, err := r.nodeKey("pvm-1"); err != nil {
		t.Fatal(err)
	}
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(aleph.deleted) != 1 || aleph.deleted[0] != hash {
		t.Fatalf("deleted = %v", aleph.deleted)
	}
	if got := cp.last(t); got.State != vmStateDeleted {
		t.Fatalf("report = %+v", got)
	}
	if _, err := os.Stat(r.nodeKeyPath("pvm-1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("node key still on disk: %v", err)
	}
}

func TestOrphanInstancesAreDeleted(t *testing.T) {
	cp := &fakeControlPlane{}
	orphan := strings.Repeat("9", 64)
	aleph := &fakeAleph{instances: []alephInstance{
		{ItemHash: orphan, Name: "virtroid-pvm-gone"},
		{ItemHash: hash, Name: "my-own-test-vm"},
	}}
	if err := testReconciler(t, cp, aleph).runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(aleph.deleted) != 1 || aleph.deleted[0] != orphan {
		t.Fatalf("deleted = %v, want only the orphan", aleph.deleted)
	}
}

func TestCLIDriverArguments(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := filepath.Join(dir, "aleph")
	// The stub records its argv and answers like the real CLI's --json mode.
	if err := os.WriteFile(script, []byte(`#!/bin/sh
printf '%s\n' "$*" >> "`+argsFile+`"
case "$*" in
  *"instance list"*) echo '[{"item_hash":"`+hash+`","name":"virtroid-pvm-1","owner":"0xabc","node_hash":"`+crnA+`","ipv6":"2001:db8:3100:1781::10","ipv4":null}]' ;;
  *"instance create"*) echo '{"item_hash":"`+hash+`","type":"INSTANCE"}'; echo '{"ready":false}' ;;
  *"instance unlock"*) echo "error: launch measurement mismatch" >&2; exit 1 ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	driver := cliDriver{bin: script, account: "provisioner", timeout: 10 * time.Second}
	ctx := context.Background()

	instances, err := driver.List(ctx)
	if err != nil || len(instances) != 1 || instances[0].IPv6 != "2001:db8:3100:1781::10" || instances[0].NodeHash != crnA {
		t.Fatalf("list = %+v err=%v", instances, err)
	}
	itemHash, err := driver.Create(ctx, createSpec{Name: "virtroid-pvm-1", CRNHash: crnA, Runtime: "r", Image: "i", VCPUs: 2, MemoryMiB: 4096, DiskMiB: 20480})
	if err != nil || itemHash != hash {
		t.Fatalf("create = %q err=%v", itemHash, err)
	}
	err = driver.Unlock(ctx, unlockSpec{ItemHash: hash, CRNHash: crnA, AMDProduct: "Genoa", PassphraseFile: "/p", Secrets: map[string]string{"b": "2", "a": "1"}})
	if !errors.Is(err, errAttestation) {
		t.Fatalf("unlock error = %v, want errAttestation", err)
	}
	if err := driver.Delete(ctx, hash); err != nil {
		t.Fatal(err)
	}

	recorded, _ := os.ReadFile(argsFile)
	lines := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	want := []string{
		"--json instance list --address provisioner",
		"--json instance create virtroid-pvm-1 --confidential --tee sev-snp --runtime r --image i --vcpus 2 --memory 4096MiB --disk-size 20480MiB --crn " + crnA + " --account provisioner",
		"--json instance unlock " + hash + " --crn " + crnA + " --amd-product Genoa --passphrase-file /p --secret a=1 --secret b=2 --account provisioner",
		"--json instance delete " + hash + " --yes --reason Virtroid runtime VM retired --account provisioner",
	}
	if len(lines) != len(want) {
		t.Fatalf("argv lines = %q", lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("call %d argv =\n  %s\nwant\n  %s", i, lines[i], want[i])
		}
	}
}

func TestParsers(t *testing.T) {
	if got := vmAddress("2001:db8:3100:1781::/64"); got != "" {
		t.Fatalf("subnet accepted as VM address: %q", got)
	}
	if got := vmAddress("2001:DB8:3100:1781::10"); got != "2001:db8:3100:1781::10" {
		t.Fatalf("vmAddress = %q", got)
	}
	for message, fatal := range map[string]bool{
		"error: launch measurement does not match the pinned value":         true,
		"SEV-SNP certificate chain or report signature verification failed": true,
		"failed to fetch VCEK certificate from AMD KDS: 429":                false,
		"error sending request: connection refused":                         false,
		"secret injection failed: something unexpected":                     false,
		"attestation report from VMPL 3 - only VMPL 0-1 are accepted":       true,
		"instance abc is still pending; try again in a few seconds":         false,
	} {
		if isAttestationFailure(message) != fatal {
			t.Errorf("isAttestationFailure(%q) = %v, want %v", message, !fatal, fatal)
		}
	}
	regions, err := parseRegionCRNs("snp-eu=" + crnA + "@Milan," + crnB + "; snp-us=" + hash)
	if err != nil || len(regions["snp-eu"]) != 2 || regions["snp-eu"][0].AMDProduct != "Milan" ||
		regions["snp-eu"][1].AMDProduct != "Genoa" || regions["snp-us"][0].Hash != hash {
		t.Fatalf("parseRegionCRNs = %+v err=%v", regions, err)
	}
	for _, bad := range []string{"", "snp-eu=nothex", "=" + crnA, "snp-eu=" + crnA + "@Zen9"} {
		if _, err := parseRegionCRNs(bad); err == nil {
			t.Errorf("parseRegionCRNs(%q) accepted", bad)
		}
	}
}
