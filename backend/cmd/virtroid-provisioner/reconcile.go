package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"virtroid/backend/internal/nodeauth"
)

const (
	vmStateRequested = "requested"
	vmStateCreating  = "creating"
	vmStateUnlocking = "unlocking"
	vmStateBooting   = "booting"
	vmStateReady     = "ready"
	vmStateDeleted   = "deleted"

	// Instance names tie Aleph instances back to provisioned VMs, so a
	// restart never creates a second instance for the same VM.
	instanceNamePrefix = "virtroid-"
	unlockRetryEvery   = 30 * time.Second
	// bootCheckEvery paces the check for VMs that restarted and wait to be
	// unlocked again.
	bootCheckEvery = 20 * time.Second
	// A VM that restarts this many times after being unlocked, before its
	// node ever comes up, is failing to boot and is retired.
	maxRestartsBeforeReady = 3
	// forgetSettles is how long a deleted instance may still be listed
	// before it is deleted again.
	forgetSettles = 10 * time.Minute
)

type crnTarget struct {
	Hash       string
	AMDProduct string
}

type provisionerConfig struct {
	VMControlPlaneURL string
	StateDir          string
	Runtime           string
	Image             string
	VCPUs             int
	MemoryMiB         int
	DiskMiB           int
	DataMiB           int
	PassphraseFile    string
	RegionCRNs        map[string][]crnTarget
}

type reconciler struct {
	cfg          provisionerConfig
	controlPlane controlPlane
	aleph        alephDriver
	now          func() time.Time
	lastUnlock   map[string]time.Time
	// created remembers instances this process submitted, in case the
	// network's listing lags behind a create whose report did not land.
	created map[string]alephInstance
	// progress, when set, reads what a VM coming up is doing so the waiting
	// user can see it; lastProgress avoids repeating unchanged notes.
	progress     progressReader
	lastProgress map[string]string
	// boots, when set, tells a VM's boots apart (see watchBoot).
	boots         bootReader
	lastBootCheck map[string]time.Time
	seenBoot      map[string]string
	restarts      map[string]int
	// forgotten holds instances deleted recently, by item hash.
	forgotten map[string]time.Time
}

func newReconciler(cfg provisionerConfig, cp controlPlane, aleph alephDriver) *reconciler {
	return &reconciler{
		cfg:           cfg,
		controlPlane:  cp,
		aleph:         aleph,
		now:           time.Now,
		lastUnlock:    map[string]time.Time{},
		created:       map[string]alephInstance{},
		lastProgress:  map[string]string{},
		lastBootCheck: map[string]time.Time{},
		seenBoot:      map[string]string{},
		restarts:      map[string]int{},
		forgotten:     map[string]time.Time{},
	}
}

// noteProgress reports a progress note for a VM when it changed.
func (r *reconciler) noteProgress(ctx context.Context, vmID, note string) {
	if note == "" || r.lastProgress[vmID] == note {
		return
	}
	if err := r.controlPlane.Report(ctx, vmID, vmReport{Progress: note}); err == nil {
		r.lastProgress[vmID] = note
	}
}

func instanceName(vmID string) string {
	return instanceNamePrefix + vmID
}

// runOnce moves every VM one step towards what the control plane wants.
func (r *reconciler) runOnce(ctx context.Context) error {
	vms, err := r.controlPlane.Work(ctx)
	if err != nil {
		return fmt.Errorf("fetch work: %w", err)
	}
	instances, err := r.aleph.List(ctx)
	if err != nil {
		return fmt.Errorf("list instances: %w", err)
	}
	byName := map[string]alephInstance{}
	byHash := map[string]alephInstance{}
	for _, instance := range instances {
		byHash[instance.ItemHash] = instance
		if instance.Name != "" {
			byName[instance.Name] = instance
		}
	}

	wanted := map[string]bool{}
	for _, vm := range vms {
		wanted[instanceName(vm.ID)] = true
		instance, found := byHash[strings.ToLower(vm.ItemHash)]
		if !found {
			instance, found = byName[instanceName(vm.ID)]
		}
		if !found {
			instance, found = r.created[vm.ID]
		}
		if err := r.step(ctx, vm, instance, found, instances); err != nil {
			log.Printf("provisioner: vm=%s state=%s desired=%s: %v", vm.ID, vm.State, vm.Desired, err)
		}
	}

	// An instance named like ours that no live VM claims is a leak: delete it.
	for _, instance := range instances {
		if strings.HasPrefix(instance.Name, instanceNamePrefix+"pvm-") && !wanted[instance.Name] {
			if r.recentlyForgotten(instance.ItemHash) {
				continue
			}
			log.Printf("provisioner: deleting orphan instance %s (%s)", instance.ItemHash, instance.Name)
			if err := r.delete(ctx, instance.ItemHash); err != nil {
				log.Printf("provisioner: delete orphan %s: %v", instance.ItemHash, err)
			}
		}
	}
	return nil
}

func (r *reconciler) step(ctx context.Context, vm provisionedVM, instance alephInstance, found bool, all []alephInstance) error {
	if vm.Desired == "absent" {
		return r.remove(ctx, vm, instance, found)
	}
	switch vm.State {
	case vmStateRequested:
		if found {
			// Created before a restart; record it instead of creating another.
			return r.controlPlane.Report(ctx, vm.ID, vmReport{State: vmStateCreating, ItemHash: instance.ItemHash, CRNHash: instance.NodeHash})
		}
		target, ok := r.pickCRN(vm.Region, all)
		if !ok {
			return r.controlPlane.Report(ctx, vm.ID, vmReport{Failure: "no compute node is configured for region " + vm.Region})
		}
		itemHash, err := r.aleph.Create(ctx, createSpec{
			Name:       instanceName(vm.ID),
			CRNHash:    target.Hash,
			Runtime:    r.cfg.Runtime,
			Image:      r.cfg.Image,
			VCPUs:      r.cfg.VCPUs,
			MemoryMiB:  r.cfg.MemoryMiB,
			DiskMiB:    r.cfg.DiskMiB,
			DataMiB:    r.cfg.DataMiB,
			AMDProduct: target.AMDProduct,
		})
		if err != nil {
			return err
		}
		r.created[vm.ID] = alephInstance{ItemHash: itemHash, Name: instanceName(vm.ID), NodeHash: target.Hash}
		log.Printf("provisioner: vm=%s created instance %s on %s", vm.ID, itemHash, target.Hash)
		return r.controlPlane.Report(ctx, vm.ID, vmReport{State: vmStateCreating, ItemHash: itemHash, CRNHash: target.Hash})

	case vmStateCreating:
		if !found || instance.IPv6 == "" {
			// Still being scheduled, downloaded or booted.
			if r.progress != nil && vm.ItemHash != "" {
				r.noteProgress(ctx, vm.ID, r.progress.Progress(ctx, vm.ItemHash))
			}
			return nil
		}
		_, publicKey, err := r.nodeKey(vm.ID)
		if err != nil {
			return err
		}
		return r.controlPlane.Report(ctx, vm.ID, vmReport{State: vmStateUnlocking, PublicIP: instance.IPv6, NodePublicKey: publicKey})

	case vmStateUnlocking:
		if last, ok := r.lastUnlock[vm.ID]; ok && r.now().Sub(last) < unlockRetryEvery {
			return nil
		}
		r.lastUnlock[vm.ID] = r.now()
		err := r.unlock(ctx, vm)
		if errors.Is(err, errAttestation) {
			log.Printf("provisioner: vm=%s failed attestation; retiring it", vm.ID)
			return r.controlPlane.Report(ctx, vm.ID, vmReport{Failure: err.Error()})
		}
		if err != nil {
			r.noteProgress(ctx, vm.ID, "Waiting for the VM's attestation agent to answer")
			return fmt.Errorf("unlock (will retry): %w", err)
		}
		delete(r.lastUnlock, vm.ID)
		log.Printf("provisioner: vm=%s attested and unlocked", vm.ID)
		r.lastProgress[vm.ID] = "Attested and unlocked; starting Docker and the Android image"
		return r.controlPlane.Report(ctx, vm.ID, vmReport{State: vmStateBooting, Progress: r.lastProgress[vm.ID]})

	case vmStateBooting, vmStateReady:
		return r.watchBoot(ctx, vm)
	}
	return nil
}

// unlock attests the VM and injects its secrets. When the VM shows the same
// boot before and after, that boot is recorded as the unlocked one.
func (r *reconciler) unlock(ctx context.Context, vm provisionedVM) error {
	privateKey, _, err := r.nodeKey(vm.ID)
	if err != nil {
		return err
	}
	dataKey, err := r.dataKey(vm.ID)
	if err != nil {
		return err
	}
	before := r.bootIdentity(ctx, vm)
	err = r.aleph.Unlock(ctx, unlockSpec{
		ItemHash:       vm.ItemHash,
		CRNHash:        vm.CRNHash,
		AMDProduct:     r.amdProduct(vm.Region, vm.CRNHash),
		PassphraseFile: r.cfg.PassphraseFile,
		Secrets: map[string]string{
			"virtroid_node_id":       vm.ID,
			"virtroid_node_key":      privateKey,
			"virtroid_data_key":      dataKey,
			"virtroid_region":        vm.Region,
			"virtroid_control_plane": r.cfg.VMControlPlaneURL,
		},
	})
	if err != nil {
		return err
	}
	if before != "" && r.bootIdentity(ctx, vm) == before {
		r.seenBoot[vm.ID] = before
		return r.saveUnlockedBoot(vm.ID, before)
	}
	// Not known which boot got the secrets: the next boot check unlocks the
	// VM again if it is waiting.
	if err := os.Remove(r.bootPath(vm.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// watchBoot unlocks a VM again after it restarts. The VM keeps its secrets
// only in memory, so after a restart (its attestation agent then presents a
// new TLS key) it waits to be unlocked. A VM that keeps restarting before its
// node ever comes up is failing to boot and is retired.
func (r *reconciler) watchBoot(ctx context.Context, vm provisionedVM) error {
	if vm.State == vmStateReady {
		delete(r.restarts, vm.ID)
	}
	if r.boots == nil || vm.PublicIP == "" {
		return nil
	}
	if last, ok := r.lastBootCheck[vm.ID]; ok && r.now().Sub(last) < bootCheckEvery {
		return nil
	}
	r.lastBootCheck[vm.ID] = r.now()
	current := r.bootIdentity(ctx, vm)
	if current == "" {
		return nil
	}
	recorded, err := r.unlockedBoot(vm.ID)
	if err != nil || recorded == current {
		return err
	}
	if recorded == "" && vm.State == vmStateReady {
		// Unlocked before its boot was recorded, and serving: this is that boot.
		r.seenBoot[vm.ID] = current
		return r.saveUnlockedBoot(vm.ID, current)
	}
	if r.seenBoot[vm.ID] != current {
		r.seenBoot[vm.ID] = current
		r.restarts[vm.ID]++
	}
	if vm.State == vmStateBooting && r.restarts[vm.ID] > maxRestartsBeforeReady {
		log.Printf("provisioner: vm=%s keeps restarting after being unlocked; retiring it", vm.ID)
		return r.controlPlane.Report(ctx, vm.ID, vmReport{
			Failure: fmt.Sprintf("the VM restarted %d times after being unlocked before its node came up", r.restarts[vm.ID]),
		})
	}
	log.Printf("provisioner: vm=%s restarted; attesting and unlocking it again", vm.ID)
	r.noteProgress(ctx, vm.ID, "The server restarted; attesting and unlocking it again")
	err = r.unlock(ctx, vm)
	if errors.Is(err, errAttestation) {
		log.Printf("provisioner: vm=%s failed attestation after a restart; retiring it", vm.ID)
		return r.controlPlane.Report(ctx, vm.ID, vmReport{Failure: err.Error()})
	}
	if err != nil {
		return fmt.Errorf("unlock after a restart (will retry): %w", err)
	}
	log.Printf("provisioner: vm=%s unlocked again", vm.ID)
	return nil
}

// bootIdentity is the VM's current boot, or "" when it cannot be read.
func (r *reconciler) bootIdentity(ctx context.Context, vm provisionedVM) string {
	if r.boots == nil {
		return ""
	}
	identity, err := r.boots.BootIdentity(ctx, vm.ItemHash, vm.PublicIP)
	if err != nil {
		return ""
	}
	return identity
}

func (r *reconciler) bootPath(vmID string) string {
	return filepath.Join(r.cfg.StateDir, "node-keys", filepath.Base(vmID)+".boot")
}

// unlockedBoot is the boot that last received the VM's secrets, if known.
func (r *reconciler) unlockedBoot(vmID string) (string, error) {
	data, err := os.ReadFile(r.bootPath(vmID))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(data)), err
}

func (r *reconciler) saveUnlockedBoot(vmID, identity string) error {
	path := r.bootPath(vmID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(identity+"\n"), 0o600)
}

func (r *reconciler) remove(ctx context.Context, vm provisionedVM, instance alephInstance, found bool) error {
	itemHash := instance.ItemHash
	if !found {
		itemHash = vm.ItemHash
	}
	if itemHash != "" && !r.recentlyForgotten(itemHash) {
		if err := r.delete(ctx, itemHash); err != nil && found {
			return fmt.Errorf("delete instance %s: %w", itemHash, err)
		}
	}
	delete(r.lastUnlock, vm.ID)
	delete(r.created, vm.ID)
	delete(r.lastProgress, vm.ID)
	delete(r.lastBootCheck, vm.ID)
	delete(r.seenBoot, vm.ID)
	delete(r.restarts, vm.ID)
	for _, path := range []string{r.nodeKeyPath(vm.ID), r.dataKeyPath(vm.ID), r.bootPath(vm.ID)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	log.Printf("provisioner: vm=%s deleted", vm.ID)
	return r.controlPlane.Report(ctx, vm.ID, vmReport{State: vmStateDeleted})
}

// delete removes an instance and remembers it, so the instance listing,
// which lags behind, does not make it send the same FORGET again.
func (r *reconciler) delete(ctx context.Context, itemHash string) error {
	if err := r.aleph.Delete(ctx, itemHash); err != nil {
		return err
	}
	r.forgotten[itemHash] = r.now()
	return nil
}

func (r *reconciler) recentlyForgotten(itemHash string) bool {
	at, ok := r.forgotten[itemHash]
	if ok && r.now().Sub(at) >= forgetSettles {
		delete(r.forgotten, itemHash)
		return false
	}
	return ok
}

// pickCRN spreads VMs over the region's compute nodes.
func (r *reconciler) pickCRN(region string, instances []alephInstance) (crnTarget, bool) {
	targets := r.cfg.RegionCRNs[region]
	if len(targets) == 0 {
		return crnTarget{}, false
	}
	load := map[string]int{}
	for _, instance := range instances {
		load[instance.NodeHash]++
	}
	best := targets[0]
	for _, target := range targets[1:] {
		if load[target.Hash] < load[best.Hash] {
			best = target
		}
	}
	return best, true
}

func (r *reconciler) amdProduct(region, crnHash string) string {
	for _, target := range r.cfg.RegionCRNs[region] {
		if target.Hash == crnHash && target.AMDProduct != "" {
			return target.AMDProduct
		}
	}
	return "Genoa"
}

func (r *reconciler) nodeKeyPath(vmID string) string {
	return filepath.Join(r.cfg.StateDir, "node-keys", filepath.Base(vmID)+".key")
}

// nodeKey returns the VM's node key (base64 PKCS#8) and public key, creating
// it on first use. Only this service and the VM it is injected into hold it.
func (r *reconciler) nodeKey(vmID string) (string, string, error) {
	path := r.nodeKeyPath(vmID)
	if data, err := os.ReadFile(path); err == nil {
		_, publicKey, err := nodeauth.LoadPrivateKey(string(data))
		if err != nil {
			return "", "", fmt.Errorf("stored node key for %s is unreadable: %w", vmID, err)
		}
		return strings.TrimSpace(string(data)), publicKey, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return "", "", err
	}
	encoded := base64.StdEncoding.EncodeToString(der)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
		return "", "", err
	}
	publicKey, err := nodeauth.PublicKeyMaterial(&privateKey.PublicKey)
	if err != nil {
		return "", "", err
	}
	return encoded, publicKey, nil
}

func (r *reconciler) dataKeyPath(vmID string) string {
	return filepath.Join(r.cfg.StateDir, "node-keys", filepath.Base(vmID)+".data")
}

// dataKey returns the VM's own key for the encrypted volume that holds user
// data, creating it on first use. Every VM boots from the same published
// disk image, so user data must not rely on that image's shared disk key.
func (r *reconciler) dataKey(vmID string) (string, error) {
	path := r.dataKeyPath(vmID)
	if data, err := os.ReadFile(path); err == nil {
		key := strings.TrimSpace(string(data))
		if decoded, err := base64.StdEncoding.DecodeString(key); err != nil || len(decoded) != 32 {
			return "", fmt.Errorf("stored data key for %s is unreadable", vmID)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	key := base64.StdEncoding.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return "", err
	}
	return key, nil
}
