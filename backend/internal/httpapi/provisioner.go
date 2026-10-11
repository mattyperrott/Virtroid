package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"virtroid/backend/internal/nodeauth"
	"virtroid/backend/internal/store"
)

// The provisioner signs its requests like a node does, under this fixed id,
// with the key configured in PROVISIONER_PUBLIC_KEY.
const provisionerRequestID = "virtroid-provisioner"

// How long the app should wait before asking again while its VM is prepared.
const provisioningRetryAfter = 15 * time.Second

func (a *API) provisioningPolicy() store.ProvisioningPolicy {
	return store.ProvisioningPolicy{
		Regions:      a.cfg.ProvisionedRegions,
		MaxVMs:       a.cfg.ProvisionerMaxVMs,
		WarmSpares:   a.cfg.ProvisionerWarmSpares,
		SetupTimeout: a.cfg.ProvisionerSetupTimeout,
		RetireAfter:  a.cfg.ProvisionerRetireAfter,
	}
}

// writeNoReadyHost answers a start that found no node. For an account whose
// region is served by dedicated VMs it requests (or finds) the account's VM
// and tells the app to retry once the VM is up.
func (a *API) writeNoReadyHost(w http.ResponseWriter, r *http.Request, accountID string, cause error) {
	if a.store == nil {
		writeRuntimeMutationError(w, cause)
		return
	}
	policy := a.provisioningPolicy()
	region, err := a.store.AccountProvisionedRegion(r.Context(), accountID, policy)
	if err != nil || region == "" {
		writeRuntimeMutationError(w, cause)
		return
	}
	vm, err := a.store.EnsureAccountVM(r.Context(), accountID, region, policy)
	if errors.Is(err, store.ErrProvisioningCapacity) {
		writeAPIError(w, http.StatusServiceUnavailable, store.ProvisioningCapacityCode, "no private runtime server is available right now")
		return
	}
	if err != nil {
		writeInternalAPIError(w, "runtime_vm_request_failed", err)
		return
	}
	retryAfter := int(provisioningRetryAfter / time.Second)
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"error":               "your private runtime server is being prepared",
		"code":                store.RuntimeHostProvisioningCode,
		"retry_after_seconds": retryAfter,
		"vm_state":            vm.State,
	})
}

func (a *API) requireProvisionerRequest(w http.ResponseWriter, r *http.Request) bool {
	if strings.TrimSpace(a.cfg.ProvisionerPublicKey) == "" || a.cfg.ProvisionerMaxVMs <= 0 {
		writeAPIError(w, http.StatusNotFound, "provisioner_disabled", "runtime VM provisioning is not enabled")
		return false
	}
	if r.Header.Get(nodeauth.HeaderNodeID) != provisionerRequestID {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "signed provisioner request headers are required"})
		return false
	}
	timestampRaw := strings.TrimSpace(r.Header.Get(nodeauth.HeaderTimestamp))
	nonce := strings.TrimSpace(r.Header.Get(nodeauth.HeaderNonce))
	bodyHash := strings.TrimSpace(r.Header.Get(nodeauth.HeaderBodySHA256))
	signature := strings.TrimSpace(r.Header.Get(nodeauth.HeaderSignature))
	timestamp, err := strconv.ParseInt(timestampRaw, 10, 64)
	if err != nil || nonce == "" || bodyHash == "" || signature == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "signed provisioner request headers are required"})
		return false
	}
	if skew := time.Since(time.Unix(timestamp, 0)); skew > 5*time.Minute || skew < -5*time.Minute {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "provisioner signature timestamp is outside the allowed window"})
		return false
	}
	body, err := readAndRestoreBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read request body"})
		return false
	}
	if nodeauth.BodyHash(body) != bodyHash {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "provisioner signature body hash mismatch"})
		return false
	}
	if err := nodeauth.Verify(
		a.cfg.ProvisionerPublicKey,
		r.Method,
		r.URL.RequestURI(),
		provisionerRequestID,
		timestampRaw,
		nonce,
		bodyHash,
		signature,
	); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "provisioner signature verification failed"})
		return false
	}
	if a.store == nil {
		writeInternalAPIError(w, "store_unavailable", errors.New("store is not configured"))
		return false
	}
	if err := a.store.RememberNodeRequestNonce(r.Context(), provisionerRequestID, nonce, time.Unix(timestamp, 0).Add(5*time.Minute)); err != nil {
		if errors.Is(err, store.ErrNodeRequestReplay) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
			return false
		}
		writeInternalAPIError(w, "provisioner_nonce_persist_failed", err)
		return false
	}
	return true
}

// provisionerWork lists the VMs the provisioner should create, unlock, watch,
// or delete.
func (a *API) provisionerWork(w http.ResponseWriter, r *http.Request) {
	if !a.requireProvisionerRequest(w, r) {
		return
	}
	vms, err := a.store.ProvisionerWork(r.Context())
	if err != nil {
		writeInternalAPIError(w, "provisioner_work_failed", err)
		return
	}
	if vms == nil {
		vms = []store.ProvisionedVM{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"vms": vms})
}

func (a *API) provisionerReport(w http.ResponseWriter, r *http.Request) {
	if !a.requireProvisionerRequest(w, r) {
		return
	}
	var req struct {
		State         string `json:"state"`
		ItemHash      string `json:"item_hash"`
		CRNHash       string `json:"crn_hash"`
		PublicIP      string `json:"public_ip"`
		NodePublicKey string `json:"node_public_key"`
		Failure       string `json:"failure"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	vm, err := a.store.ApplyProvisionerReport(r.Context(), r.PathValue("id"), store.ProvisionerReport{
		State:         req.State,
		ItemHash:      req.ItemHash,
		CRNHash:       req.CRNHash,
		PublicIP:      req.PublicIP,
		NodePublicKey: req.NodePublicKey,
		Failure:       req.Failure,
	})
	switch {
	case errors.Is(err, store.ErrProvisionedVMNotFound):
		writeAPIError(w, http.StatusNotFound, "provisioned_vm_not_found", err.Error())
	case errors.Is(err, store.ErrProvisionedVMTransition):
		writeAPIError(w, http.StatusConflict, "provisioned_vm_transition_rejected", err.Error())
	case err != nil:
		log.Printf("provisioner report rejected vm=%s state=%s", r.PathValue("id"), req.State)
		writeAPIError(w, http.StatusBadRequest, "provisioner_report_rejected", err.Error())
	default:
		writeJSON(w, http.StatusOK, vm)
	}
}

func readAndRestoreBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}
