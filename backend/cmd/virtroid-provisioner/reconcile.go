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
	vmStateDeleted   = "deleted"

	// Instance names tie Aleph instances back to provisioned VMs, so a
	// restart never creates a second instance for the same VM.
	instanceNamePrefix = "virtroid-"
	unlockRetryEvery   = 30 * time.Second
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
}

func newReconciler(cfg provisionerConfig, cp controlPlane, aleph alephDriver) *reconciler {
	return &reconciler{
		cfg:          cfg,
		controlPlane: cp,
		aleph:        aleph,
		now:          time.Now,
		lastUnlock:   map[string]time.Time{},
		created:      map[string]alephInstance{},
		lastProgress: map[string]string{},
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
			log.Printf("provisioner: deleting orphan instance %s (%s)", instance.ItemHash, instance.Name)
			if err := r.aleph.Delete(ctx, instance.ItemHash); err != nil {
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
		privateKey, _, err := r.nodeKey(vm.ID)
		if err != nil {
			return err
		}
		dataKey, err := r.dataKey(vm.ID)
		if err != nil {
			return err
		}
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
	}
	return nil
}

func (r *reconciler) remove(ctx context.Context, vm provisionedVM, instance alephInstance, found bool) error {
	itemHash := instance.ItemHash
	if !found {
		itemHash = vm.ItemHash
	}
	if itemHash != "" {
		if err := r.aleph.Delete(ctx, itemHash); err != nil && found {
			return fmt.Errorf("delete instance %s: %w", itemHash, err)
		}
	}
	delete(r.lastUnlock, vm.ID)
	delete(r.created, vm.ID)
	delete(r.lastProgress, vm.ID)
	for _, path := range []string{r.nodeKeyPath(vm.ID), r.dataKeyPath(vm.ID)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	log.Printf("provisioner: vm=%s deleted", vm.ID)
	return r.controlPlane.Report(ctx, vm.ID, vmReport{State: vmStateDeleted})
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
