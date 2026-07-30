package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	deviceSignatureContext = "VIRTROID-DEVICE-SIGNATURE-V1"
	blobEnvelopeContext    = "VIRTROID-BLOB-KEY-ENVELOPE-V1"
	blobEnvelopeAlgorithm  = "P256_ECDH_HKDF_SHA256_AESGCM_V1"
	blobEnvelopeHKDFInfo   = "virtroid-blob-key-envelope-v1"
	blobVerifierPrefix     = "virtroid-blob-verifier-v1:"
)

type qaClient struct {
	baseURL    string
	http       *http.Client
	deviceKey  *ecdsa.PrivateKey
	accountID  string
	deviceID   string
	bootstrapInvite string
	blobKey    []byte
	verifier   string
	runtimeID  string
	runtimeIDs []string
}

type runtimeState struct {
	ID                  string          `json:"id"`
	Status              string          `json:"status"`
	DesiredState        string          `json:"desired_state"`
	ConnectionStatus    string          `json:"connection_status"`
	PersonaVersion      int             `json:"persona_version"`
	OperationGeneration int64           `json:"operation_generation"`
	BlobManifestJSON    json.RawMessage `json:"blob_manifest_json"`
}

type lease struct {
	LeaseID       string `json:"lease_id"`
	RuntimeID     string `json:"runtime_id"`
	HostID        string `json:"host_id"`
	Operation     string `json:"operation"`
	Algorithm     string `json:"algorithm"`
	NodePublicKey string `json:"node_public_key"`
}

type operationResult struct {
	Action string `json:"action"`
	Status int    `json:"status"`
	Code   string `json:"code,omitempty"`
	Error  string `json:"error,omitempty"`
}

type report struct {
	StartedAt                time.Time                 `json:"started_at"`
	FinishedAt               time.Time                 `json:"finished_at"`
	BaseURL                  string                    `json:"base_url"`
	AccountID                string                    `json:"account_id"`
	DeviceID                 string                    `json:"device_id"`
	RuntimeID                string                    `json:"runtime_id"`
	ConcurrentStart          []operationResult         `json:"concurrent_start"`
	ConcurrentStop           []operationResult         `json:"concurrent_stop"`
	MixedStartStop           []operationResult         `json:"mixed_start_stop"`
	ConcurrentWipe           []operationResult         `json:"concurrent_wipe"`
	ConcurrentDelete         []operationResult         `json:"concurrent_delete"`
	States                   map[string]runtimeState   `json:"states"`
	Checks                   map[string]bool           `json:"checks"`
	Notes                    []string                  `json:"notes,omitempty"`
	HTTPStatusCounts         map[string]map[string]int `json:"http_status_counts"`
	SnapshotGenerationByStep map[string]int64          `json:"snapshot_generation_by_step"`
}

func main() {
	defer func() {
		if recovered := recover(); recovered != nil {
			if failure, ok := recovered.(fatalFailure); ok {
				fmt.Fprintln(os.Stderr, "lifecycleqa:", failure.err)
				os.Exit(1)
			}
			panic(recovered)
		}
	}()

	var (
		baseURL    = flag.String("base-url", "https://virtroid.network", "Virtroid control-plane base URL")
		bootstrapInviteEnv = flag.String("bootstrap-invite-env", "VIRTROID_BOOTSTRAP_INVITE", "environment variable containing the one-time bootstrap invitation")
		concurrent = flag.Int("concurrency", 8, "number of concurrent duplicate mutations")
		confirm    = flag.Bool("confirm-disposable", false, "required acknowledgement that the generated account is disposable")
		scenario   = flag.String("scenario", "lifecycle", "test scenario: lifecycle, snapshot-corruption, or quota")
		timeout    = flag.Duration("timeout", 4*time.Minute, "maximum wait per lifecycle transition")
	)
	flag.Parse()
	if !*confirm {
		fmt.Fprintln(os.Stderr, "refusing to run without --confirm-disposable")
		os.Exit(2)
	}
	if *concurrent < 2 || *concurrent > 16 {
		fmt.Fprintln(os.Stderr, "--concurrency must be between 2 and 16")
		os.Exit(2)
	}
	if *scenario != "lifecycle" && *scenario != "snapshot-corruption" && *scenario != "quota" {
		fmt.Fprintln(os.Stderr, "--scenario must be lifecycle, snapshot-corruption, or quota")
		os.Exit(2)
	}
	bootstrapInviteEnvName := strings.TrimSpace(*bootstrapInviteEnv)
	if bootstrapInviteEnvName == "" {
		fmt.Fprintln(os.Stderr, "--bootstrap-invite-env must name an environment variable")
		os.Exit(2)
	}
	bootstrapInvite := strings.TrimSpace(os.Getenv(bootstrapInviteEnvName))
	if bootstrapInvite == "" {
		fmt.Fprintf(os.Stderr, "%s must contain a one-time bootstrap invitation\n", bootstrapInviteEnvName)
		os.Exit(2)
	}

	ctx := context.Background()
	client, err := newQAClient(*baseURL, bootstrapInvite)
	if err != nil {
		fatal(err)
	}
	rep := report{
		StartedAt:                time.Now().UTC(),
		BaseURL:                  client.baseURL,
		AccountID:                client.accountID,
		DeviceID:                 client.deviceID,
		States:                   make(map[string]runtimeState),
		Checks:                   make(map[string]bool),
		HTTPStatusCounts:         make(map[string]map[string]int),
		SnapshotGenerationByStep: make(map[string]int64),
	}
	cleanupNeeded := false
	defer func() {
		if cleanupNeeded {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			client.cleanup(cleanupCtx)
		}
	}()

	if err := client.bootstrap(ctx); err != nil {
		fatal(err)
	}
	cleanupNeeded = true
	if err := client.registerIdentity(ctx); err != nil {
		fatal(err)
	}
	created, err := client.createRuntime(ctx, "Lifecycle QA "+client.runtimeSuffix())
	if err != nil {
		fatal(err)
	}
	client.runtimeID = created.ID
	rep.RuntimeID = created.ID
	rep.States["created"] = created
	fmt.Fprintf(os.Stderr, "lifecycleqa disposable account=%s runtime=%s\n", client.accountID, client.runtimeID)
	if *scenario == "snapshot-corruption" {
		if err := client.runSnapshotCorruptionScenario(ctx, &rep, *timeout); err != nil {
			fatal(err)
		}
		if err := client.deleteAccount(ctx); err != nil {
			fatal(err)
		}
		cleanupNeeded = false
		rep.Checks["account_delete_accepted"] = true
		for name, ok := range rep.Checks {
			if !ok {
				fatal(fmt.Errorf("check failed: %s", name))
			}
		}
		rep.FinishedAt = time.Now().UTC()
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(rep); err != nil {
			fatal(err)
		}
		return
	}
	if *scenario == "quota" {
		if err := client.runQuotaScenario(ctx, &rep, *timeout); err != nil {
			fatal(err)
		}
		if err := client.deleteAccount(ctx); err != nil {
			fatal(err)
		}
		cleanupNeeded = false
		rep.Checks["account_delete_accepted"] = true
		for name, ok := range rep.Checks {
			if !ok {
				fatal(fmt.Errorf("check failed: %s", name))
			}
		}
		rep.FinishedAt = time.Now().UTC()
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(rep); err != nil {
			fatal(err)
		}
		return
	}

	rep.ConcurrentStart = client.concurrentMutations(ctx, repeat("start", *concurrent))
	recordStatuses(&rep, "concurrent_start", rep.ConcurrentStart)
	assertNoServerErrors(rep.ConcurrentStart)
	running, err := client.waitRuntime(ctx, *timeout, func(state runtimeState) bool {
		return state.Status == "running" && state.DesiredState == "running" && state.ConnectionStatus == "online"
	})
	if err != nil {
		fatal(err)
	}
	rep.States["first_running"] = running
	rep.Checks["duplicate_start_single_generation"] = running.OperationGeneration == created.OperationGeneration+1
	rep.Checks["duplicate_start_single_persona_rotation"] = running.PersonaVersion == created.PersonaVersion+1

	rep.ConcurrentStop = client.concurrentMutations(ctx, repeat("stop", *concurrent))
	recordStatuses(&rep, "concurrent_stop", rep.ConcurrentStop)
	assertNoServerErrors(rep.ConcurrentStop)
	stoppedOne, err := client.waitRuntime(ctx, *timeout, isStopped)
	if err != nil {
		fatal(err)
	}
	rep.States["first_stopped"] = stoppedOne
	rep.SnapshotGenerationByStep["first_stopped"] = manifestGeneration(stoppedOne.BlobManifestJSON)
	rep.Checks["first_snapshot_created"] = manifestGeneration(stoppedOne.BlobManifestJSON) == 1

	mixed := make([]string, 0, *concurrent)
	for index := 0; index < *concurrent; index++ {
		if index%2 == 0 {
			mixed = append(mixed, "start")
		} else {
			mixed = append(mixed, "stop")
		}
	}
	rep.MixedStartStop = client.concurrentMutations(ctx, mixed)
	recordStatuses(&rep, "mixed_start_stop", rep.MixedStartStop)
	assertNoServerErrors(rep.MixedStartStop)
	if _, err := client.mutate(ctx, "stop"); err != nil {
		fatal(fmt.Errorf("reconcile mixed race to stopped: %w", err))
	}
	stoppedRace, err := client.waitRuntime(ctx, *timeout, isStopped)
	if err != nil {
		fatal(err)
	}
	rep.States["mixed_race_reconciled"] = stoppedRace
	rep.Checks["mixed_race_reconciled"] = stoppedRace.DesiredState == "stopped"

	if _, err := client.mutate(ctx, "start"); err != nil {
		fatal(fmt.Errorf("restore start: %w", err))
	}
	restored, err := client.waitRuntime(ctx, *timeout, func(state runtimeState) bool {
		return state.Status == "running" && state.ConnectionStatus == "online"
	})
	if err != nil {
		fatal(err)
	}
	rep.States["restored_running"] = restored
	rep.Checks["restore_advanced_persona"] = restored.PersonaVersion > running.PersonaVersion
	if _, err := client.mutate(ctx, "stop"); err != nil {
		fatal(err)
	}
	stoppedTwo, err := client.waitRuntime(ctx, *timeout, isStopped)
	if err != nil {
		fatal(err)
	}
	rep.States["second_stopped"] = stoppedTwo
	rep.SnapshotGenerationByStep["second_stopped"] = manifestGeneration(stoppedTwo.BlobManifestJSON)
	rep.Checks["snapshot_generation_advanced"] = manifestGeneration(stoppedTwo.BlobManifestJSON) > manifestGeneration(stoppedOne.BlobManifestJSON)

	rep.ConcurrentWipe = client.concurrentMutations(ctx, repeat("wipe", *concurrent))
	recordStatuses(&rep, "concurrent_wipe", rep.ConcurrentWipe)
	assertNoServerErrors(rep.ConcurrentWipe)
	wiped, err := client.waitRuntime(ctx, *timeout, func(state runtimeState) bool {
		return isStopped(state) && manifestGeneration(state.BlobManifestJSON) == 0
	})
	if err != nil {
		fatal(err)
	}
	rep.States["wiped"] = wiped
	rep.Checks["wipe_cleared_snapshot"] = manifestGeneration(wiped.BlobManifestJSON) == 0

	if _, err := client.mutate(ctx, "start"); err != nil {
		fatal(fmt.Errorf("post-wipe start: %w", err))
	}
	postWipe, err := client.waitRuntime(ctx, *timeout, func(state runtimeState) bool {
		return state.Status == "running" && state.ConnectionStatus == "online"
	})
	if err != nil {
		fatal(err)
	}
	rep.States["post_wipe_running"] = postWipe
	rep.Checks["post_wipe_fresh_persona"] = postWipe.PersonaVersion > wiped.PersonaVersion
	if _, err := client.mutate(ctx, "stop"); err != nil {
		fatal(err)
	}
	if _, err := client.waitRuntime(ctx, *timeout, isStopped); err != nil {
		fatal(err)
	}

	rep.ConcurrentDelete = client.concurrentMutations(ctx, repeat("delete", *concurrent))
	recordStatuses(&rep, "concurrent_delete", rep.ConcurrentDelete)
	assertNoServerErrors(rep.ConcurrentDelete)
	if err := client.waitRuntimeGone(ctx, *timeout); err != nil {
		fatal(err)
	}
	rep.Checks["runtime_deleted"] = true
	if err := client.deleteAccount(ctx); err != nil {
		fatal(err)
	}
	cleanupNeeded = false
	rep.Checks["account_delete_accepted"] = true

	for name, ok := range rep.Checks {
		if !ok {
			fatal(fmt.Errorf("check failed: %s", name))
		}
	}
	rep.FinishedAt = time.Now().UTC()
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(rep); err != nil {
		fatal(err)
	}
}

func (c *qaClient) runSnapshotCorruptionScenario(ctx context.Context, rep *report, timeout time.Duration) error {
	if _, err := c.mutate(ctx, "start"); err != nil {
		return fmt.Errorf("snapshot-corruption initial start: %w", err)
	}
	running, err := c.waitRuntime(ctx, timeout, func(state runtimeState) bool {
		return state.Status == "running" && state.DesiredState == "running" && state.ConnectionStatus == "online"
	})
	if err != nil {
		return err
	}
	rep.States["corruption_test_running"] = running
	if _, err := c.mutate(ctx, "stop"); err != nil {
		return fmt.Errorf("snapshot-corruption initial stop: %w", err)
	}
	stopped, err := c.waitRuntime(ctx, timeout, isStopped)
	if err != nil {
		return err
	}
	rep.States["corruption_test_stopped"] = stopped
	rep.SnapshotGenerationByStep["before_corruption"] = manifestGeneration(stopped.BlobManifestJSON)
	chunkKey := manifestFirstChunkKey(stopped.BlobManifestJSON)
	if manifestGeneration(stopped.BlobManifestJSON) != 1 || chunkKey == "" {
		return errors.New("snapshot-corruption scenario did not create a usable generation-1 snapshot")
	}
	fmt.Fprintf(os.Stderr, "lifecycleqa pause=corrupt chunk=%s\n", chunkKey)
	if err := waitForFaultSignal("corrupted"); err != nil {
		return err
	}

	if _, err := c.mutate(ctx, "start"); err != nil {
		return fmt.Errorf("start with corrupted snapshot: %w", err)
	}
	rejected, err := c.waitRuntime(ctx, timeout, func(state runtimeState) bool {
		return state.Status == "error" && state.DesiredState == "running" && state.ConnectionStatus == "offline"
	})
	if err != nil {
		return fmt.Errorf("wait for corrupted snapshot rejection: %w", err)
	}
	rep.States["corrupted_snapshot_rejected"] = rejected
	rep.Checks["corrupted_snapshot_rejected"] = rejected.Status == "error"
	rep.Checks["corruption_did_not_change_snapshot_generation"] = manifestGeneration(rejected.BlobManifestJSON) == 1
	fmt.Fprintln(os.Stderr, "lifecycleqa pause=restore corruption_rejected=true")
	if err := waitForFaultSignal("restored"); err != nil {
		return err
	}

	recovered, err := c.waitRuntime(ctx, timeout, func(state runtimeState) bool {
		return state.Status == "running" && state.DesiredState == "running" && state.ConnectionStatus == "online"
	})
	if err != nil {
		return fmt.Errorf("wait for repaired snapshot recovery: %w", err)
	}
	rep.States["repaired_snapshot_running"] = recovered
	rep.Checks["repaired_snapshot_recovered"] = true
	if _, err := c.mutate(ctx, "stop"); err != nil {
		return fmt.Errorf("stop after repaired snapshot recovery: %w", err)
	}
	finalStopped, err := c.waitRuntime(ctx, timeout, isStopped)
	if err != nil {
		return err
	}
	rep.States["repaired_snapshot_stopped"] = finalStopped
	rep.SnapshotGenerationByStep["after_recovery"] = manifestGeneration(finalStopped.BlobManifestJSON)
	rep.Checks["snapshot_generation_advanced_after_recovery"] = manifestGeneration(finalStopped.BlobManifestJSON) == 2
	if _, err := c.mutate(ctx, "delete"); err != nil {
		return fmt.Errorf("delete snapshot-corruption runtime: %w", err)
	}
	if err := c.waitRuntimeGone(ctx, timeout); err != nil {
		return err
	}
	rep.Checks["runtime_deleted"] = true
	return nil
}

func (c *qaClient) runQuotaScenario(ctx context.Context, rep *report, timeout time.Duration) error {
	firstRuntimeID := c.runtimeID
	fmt.Fprintln(os.Stderr, "lifecycleqa pause=quota-baseline")
	if err := waitForFaultSignal("quota-baseline-ready"); err != nil {
		return err
	}

	second, err := c.createRuntime(ctx, "Lifecycle QA quota second "+c.runtimeSuffix())
	if err != nil {
		return fmt.Errorf("create second quota runtime: %w", err)
	}
	secondRuntimeID := second.ID
	rep.States["quota_second_created"] = second
	status, code, err := c.createRuntimeExpectFailure(ctx, "Lifecycle QA quota third "+c.runtimeSuffix())
	if err != nil {
		return err
	}
	rep.Checks["runtime_count_quota_enforced"] = status == http.StatusConflict && code == "runtime_quota_exceeded"

	c.runtimeID = firstRuntimeID
	if _, err := c.mutate(ctx, "start"); err != nil {
		return fmt.Errorf("start first quota runtime: %w", err)
	}
	firstRunning, err := c.waitRuntime(ctx, timeout, func(state runtimeState) bool {
		return state.Status == "running" && state.ConnectionStatus == "online"
	})
	if err != nil {
		return err
	}
	rep.States["quota_first_running"] = firstRunning
	c.runtimeID = secondRuntimeID
	activeResult, activeErr := c.mutate(ctx, "start")
	rep.Checks["active_runtime_quota_enforced"] = activeErr != nil &&
		activeResult.Status == http.StatusConflict && activeResult.Code == "active_runtime_quota_exceeded"
	c.runtimeID = firstRuntimeID
	if _, err := c.mutate(ctx, "stop"); err != nil {
		return fmt.Errorf("stop first quota runtime: %w", err)
	}
	firstStopped, err := c.waitRuntime(ctx, timeout, isStopped)
	if err != nil {
		return err
	}
	rep.States["quota_first_stopped"] = firstStopped

	fmt.Fprintln(os.Stderr, "lifecycleqa pause=start-quota")
	if err := waitForFaultSignal("start-quota-ready"); err != nil {
		return err
	}
	c.runtimeID = secondRuntimeID
	startResult, startErr := c.mutate(ctx, "start")
	rep.Checks["daily_start_quota_enforced"] = startErr != nil &&
		startResult.Status == http.StatusTooManyRequests && startResult.Code == "runtime_start_quota_exceeded"

	fmt.Fprintln(os.Stderr, "lifecycleqa pause=trial-quota")
	if err := waitForFaultSignal("trial-quota-ready"); err != nil {
		return err
	}
	trialResult, trialErr := c.mutate(ctx, "start")
	rep.Checks["trial_time_quota_enforced"] = trialErr != nil &&
		trialResult.Status == http.StatusPaymentRequired && trialResult.Code == "runtime_trial_time_exceeded"

	fmt.Fprintln(os.Stderr, "lifecycleqa pause=storage-quota")
	if err := waitForFaultSignal("storage-quota-ready"); err != nil {
		return err
	}
	c.runtimeID = firstRuntimeID
	if _, err := c.mutate(ctx, "start"); err != nil {
		return fmt.Errorf("start storage-quota runtime: %w", err)
	}
	if _, err := c.waitRuntime(ctx, timeout, func(state runtimeState) bool {
		return state.Status == "running" && state.ConnectionStatus == "online"
	}); err != nil {
		return err
	}
	if _, err := c.mutate(ctx, "stop"); err != nil {
		return fmt.Errorf("stop storage-quota runtime: %w", err)
	}
	storageRejected, err := c.waitRuntime(ctx, timeout, func(state runtimeState) bool {
		return state.Status == "error" && state.DesiredState == "stopped" && state.ConnectionStatus == "offline"
	})
	if err != nil {
		return fmt.Errorf("wait for storage quota rejection: %w", err)
	}
	rep.States["storage_quota_rejected"] = storageRejected
	rep.Checks["storage_quota_enforced"] = storageRejected.Status == "error" &&
		manifestGeneration(storageRejected.BlobManifestJSON) == manifestGeneration(firstStopped.BlobManifestJSON)
	fmt.Fprintln(os.Stderr, "lifecycleqa pause=storage-quota-raise")
	if err := waitForFaultSignal("storage-quota-raised"); err != nil {
		return err
	}
	storageRecovered, err := c.waitRuntime(ctx, timeout, isStopped)
	if err != nil {
		return fmt.Errorf("wait for storage quota recovery: %w", err)
	}
	rep.States["storage_quota_recovered"] = storageRecovered
	rep.Checks["storage_quota_recovered"] = manifestGeneration(storageRecovered.BlobManifestJSON) >
		manifestGeneration(firstStopped.BlobManifestJSON)

	for _, runtimeID := range []string{firstRuntimeID, secondRuntimeID} {
		c.runtimeID = runtimeID
		if _, err := c.mutate(ctx, "delete"); err != nil {
			return fmt.Errorf("delete quota runtime %s: %w", runtimeID, err)
		}
		if err := c.waitRuntimeGone(ctx, timeout); err != nil {
			return err
		}
	}
	rep.Checks["runtimes_deleted"] = true
	return nil
}

func (c *qaClient) createRuntimeExpectFailure(ctx context.Context, name string) (int, string, error) {
	body := map[string]any{
		"account_id": c.accountID, "device_id": c.deviceID, "name": name,
		"width_px": 800, "height_px": 1600, "density_dpi": 320,
		"audio_enabled": true, "camera_mode": "disabled", "file_mode": "upload-only",
		"blob_auto_snapshot": true, "blob_retain_days": 7,
	}
	status, payload, err := c.doSigned(ctx, http.MethodPost, "/api/v1/me/runtimes", body)
	if err != nil {
		return 0, "", err
	}
	if status >= 200 && status < 300 {
		return status, "", fmt.Errorf("create runtime unexpectedly succeeded: status=%d", status)
	}
	result := decodeOperationResult("create", status, payload)
	return status, result.Code, nil
}

func waitForFaultSignal(expected string) error {
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return err
		}
		return fmt.Errorf("waiting for %q signal: stdin closed", expected)
	}
	if actual := strings.TrimSpace(scanner.Text()); actual != expected {
		return fmt.Errorf("waiting for %q signal: got %q", expected, actual)
	}
	return nil
}

func newQAClient(rawBaseURL string, bootstrapInvite string) (*qaClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" {
		return nil, errors.New("--base-url must be an HTTPS origin without a path")
	}
	deviceKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	blobKey := make([]byte, 32)
	if _, err := rand.Read(blobKey); err != nil {
		return nil, err
	}
	verifierDigest := sha256.Sum256(append([]byte(blobVerifierPrefix), blobKey...))
	return &qaClient{
		baseURL:   strings.TrimRight(parsed.String(), "/"),
		http:      &http.Client{Timeout: 2 * time.Minute},
		deviceKey: deviceKey,
		accountID: uuid.NewString(),
		deviceID:  uuid.NewString(),
		bootstrapInvite: strings.TrimSpace(bootstrapInvite),
		blobKey:   blobKey,
		verifier:  base64.RawURLEncoding.EncodeToString(verifierDigest[:]),
	}, nil
}

func (c *qaClient) bootstrap(ctx context.Context) error {
	publicDER, err := x509.MarshalPKIXPublicKey(&c.deviceKey.PublicKey)
	if err != nil {
		return err
	}
	body := map[string]any{
		"account_id":   c.accountID,
		"device_id":    c.deviceID,
		"device_name":  "Lifecycle QA",
		"public_key":   base64.StdEncoding.EncodeToString(publicDER),
		"invite_token": c.bootstrapInvite,
		"runtime_name": "Lifecycle QA",
		"width_px":     800,
		"height_px":    1600,
		"density_dpi":  320,
	}
	status, payload, err := c.doSigned(ctx, http.MethodPost, "/api/v1/bootstrap", body)
	if err != nil {
		return err
	}
	if status != http.StatusCreated {
		return responseError("bootstrap", status, payload)
	}
	c.bootstrapInvite = ""
	return nil
}

func (c *qaClient) registerIdentity(ctx context.Context) error {
	body := map[string]any{"account_id": c.accountID, "device_id": c.deviceID, "blob_key_verifier": c.verifier}
	status, payload, err := c.doSigned(ctx, http.MethodPost, "/api/v1/me/identity/register", body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return responseError("register identity", status, payload)
	}
	return nil
}

func (c *qaClient) createRuntime(ctx context.Context, name string) (runtimeState, error) {
	body := map[string]any{
		"account_id": c.accountID, "device_id": c.deviceID, "name": name,
		"width_px": 800, "height_px": 1600, "density_dpi": 320,
		"audio_enabled": true, "camera_mode": "disabled", "file_mode": "upload-only",
		"blob_auto_snapshot": true, "blob_retain_days": 7,
	}
	status, payload, err := c.doSigned(ctx, http.MethodPost, "/api/v1/me/runtimes", body)
	if err != nil {
		return runtimeState{}, err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return runtimeState{}, responseError("create runtime", status, payload)
	}
	var state runtimeState
	err = json.Unmarshal(payload, &state)
	if err == nil && state.ID != "" {
		c.runtimeIDs = append(c.runtimeIDs, state.ID)
	}
	return state, err
}

func (c *qaClient) mutate(ctx context.Context, action string) (operationResult, error) {
	leasePath := fmt.Sprintf("/api/v1/me/runtimes/%s/blob-key-lease", c.runtimeID)
	status, payload, err := c.doSigned(ctx, http.MethodPost, leasePath, map[string]any{
		"account_id": c.accountID, "device_id": c.deviceID, "operation": action, "blob_key_verifier": c.verifier,
	})
	if err != nil {
		return operationResult{Action: action}, err
	}
	if status != http.StatusCreated {
		result := decodeOperationResult(action, status, payload)
		return result, responseError(action+" lease", status, payload)
	}
	var granted lease
	if err := json.Unmarshal(payload, &granted); err != nil {
		return operationResult{Action: action}, err
	}
	envelope, err := encryptEnvelope(c.blobKey, granted)
	if err != nil {
		return operationResult{Action: action}, err
	}
	actionPath := fmt.Sprintf("/api/v1/me/runtimes/%s/%s", c.runtimeID, action)
	status, payload, err = c.doSigned(ctx, http.MethodPost, actionPath, map[string]any{
		"account_id": c.accountID, "device_id": c.deviceID,
		"blob_key_verifier": c.verifier, "blob_key_envelope": envelope,
	})
	if err != nil {
		return operationResult{Action: action}, err
	}
	result := decodeOperationResult(action, status, payload)
	if status < 200 || status >= 300 {
		return result, responseError(action, status, payload)
	}
	return result, nil
}

func (c *qaClient) concurrentMutations(ctx context.Context, actions []string) []operationResult {
	results := make([]operationResult, len(actions))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for index, action := range actions {
		wg.Add(1)
		go func(index int, action string) {
			defer wg.Done()
			<-start
			result, err := c.mutate(ctx, action)
			if err != nil && result.Status == 0 {
				result.Error = err.Error()
			}
			results[index] = result
		}(index, action)
	}
	close(start)
	wg.Wait()
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Action == results[j].Action {
			return results[i].Status < results[j].Status
		}
		return results[i].Action < results[j].Action
	})
	return results
}

func (c *qaClient) runtime(ctx context.Context) (runtimeState, int, error) {
	path := fmt.Sprintf("/api/v1/me/runtimes/%s/state?account_id=%s&device_id=%s", c.runtimeID, c.accountID, c.deviceID)
	status, payload, err := c.doSigned(ctx, http.MethodGet, path, nil)
	if err != nil {
		return runtimeState{}, 0, err
	}
	if status == http.StatusNotFound {
		return runtimeState{}, status, nil
	}
	if status != http.StatusOK {
		return runtimeState{}, status, responseError("runtime state", status, payload)
	}
	var wrapper struct {
		Runtime runtimeState `json:"runtime"`
	}
	if err := json.Unmarshal(payload, &wrapper); err != nil {
		return runtimeState{}, status, err
	}
	return wrapper.Runtime, status, nil
}

func (c *qaClient) waitRuntime(ctx context.Context, timeout time.Duration, ready func(runtimeState) bool) (runtimeState, error) {
	deadline := time.Now().Add(timeout)
	var latest runtimeState
	for time.Now().Before(deadline) {
		state, status, err := c.runtime(ctx)
		if err == nil && status == http.StatusOK {
			latest = state
			if ready(state) {
				return state, nil
			}
		}
		select {
		case <-ctx.Done():
			return latest, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return latest, fmt.Errorf("runtime transition timeout: status=%s desired=%s connection=%s generation=%d", latest.Status, latest.DesiredState, latest.ConnectionStatus, latest.OperationGeneration)
}

func (c *qaClient) waitRuntimeGone(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, status, err := c.runtime(ctx)
		if status == http.StatusNotFound {
			return nil
		}
		if err == nil && (state.Status == "deleted" || state.DesiredState == "deleted") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return errors.New("runtime deletion timeout")
}

func (c *qaClient) deleteAccount(ctx context.Context) error {
	path := fmt.Sprintf("/api/v1/me?account_id=%s&device_id=%s", c.accountID, c.deviceID)
	status, payload, err := c.doSigned(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	if status < 200 || status >= 300 {
		return responseError("delete account", status, payload)
	}
	return nil
}

func (c *qaClient) cleanup(ctx context.Context) {
	for index := len(c.runtimeIDs) - 1; index >= 0; index-- {
		c.runtimeID = c.runtimeIDs[index]
		if state, status, err := c.runtime(ctx); err == nil && status == http.StatusOK {
			if !isStopped(state) {
				_, _ = c.mutate(ctx, "stop")
				_, _ = c.waitRuntime(ctx, 60*time.Second, isStopped)
			}
			_, _ = c.mutate(ctx, "delete")
			_ = c.waitRuntimeGone(ctx, 60*time.Second)
		}
	}
	if err := c.deleteAccount(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "lifecycleqa cleanup:", err)
	}
}

func (c *qaClient) doSigned(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
	} else if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		payload = []byte("{}")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	if len(payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := c.sign(req, path, payload); err != nil {
		return 0, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return resp.StatusCode, responseBody, err
}

func (c *qaClient) sign(req *http.Request, requestURI string, body []byte) error {
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonce := uuid.NewString()
	bodyDigest := sha256.Sum256(body)
	bodyHash := base64.RawURLEncoding.EncodeToString(bodyDigest[:])
	canonical := strings.Join([]string{
		deviceSignatureContext, strings.ToUpper(req.Method), requestURI,
		c.accountID, c.deviceID, timestamp, nonce, bodyHash,
	}, "\n")
	digest := sha256.Sum256([]byte(canonical))
	signature, err := ecdsa.SignASN1(rand.Reader, c.deviceKey, digest[:])
	if err != nil {
		return err
	}
	req.Header.Set("X-Virtroid-Account-ID", c.accountID)
	req.Header.Set("X-Virtroid-Device-ID", c.deviceID)
	req.Header.Set("X-Virtroid-Timestamp", timestamp)
	req.Header.Set("X-Virtroid-Nonce", nonce)
	req.Header.Set("X-Virtroid-Body-SHA256", bodyHash)
	req.Header.Set("X-Virtroid-Signature", base64.RawURLEncoding.EncodeToString(signature))
	return nil
}

func encryptEnvelope(blobKey []byte, granted lease) (map[string]any, error) {
	if granted.Algorithm != blobEnvelopeAlgorithm {
		return nil, fmt.Errorf("unsupported envelope algorithm %q", granted.Algorithm)
	}
	nodeDER, err := base64.StdEncoding.DecodeString(strings.TrimSpace(granted.NodePublicKey))
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKIXPublicKey(nodeDER)
	if err != nil {
		return nil, err
	}
	nodeKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok || nodeKey.Curve != elliptic.P256() {
		return nil, errors.New("node key is not P-256")
	}
	ephemeral, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	ephemeralECDH, err := ephemeral.ECDH()
	if err != nil {
		return nil, err
	}
	nodeECDH, err := nodeKey.ECDH()
	if err != nil {
		return nil, err
	}
	secret, err := ephemeralECDH.ECDH(nodeECDH)
	if err != nil {
		return nil, err
	}
	aad := []byte(strings.Join([]string{
		blobEnvelopeContext, granted.LeaseID, granted.Operation, granted.RuntimeID, granted.HostID,
	}, "\n"))
	salt := sha256.Sum256(aad)
	wrappingKey := hkdfSHA256(secret, salt[:], []byte(blobEnvelopeHKDFInfo), 32)
	block, err := aes.NewCipher(wrappingKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, iv, blobKey, aad)
	ephemeralDER, err := x509.MarshalPKIXPublicKey(&ephemeral.PublicKey)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"version": 1, "algorithm": blobEnvelopeAlgorithm,
		"lease_id": granted.LeaseID, "operation": granted.Operation,
		"runtime_id": granted.RuntimeID, "host_id": granted.HostID,
		"ephemeral_public_key": base64.StdEncoding.EncodeToString(ephemeralDER),
		"iv":                   base64.StdEncoding.EncodeToString(iv),
		"ciphertext":           base64.StdEncoding.EncodeToString(ciphertext),
	}, nil
}

func hkdfSHA256(secret, salt, info []byte, length int) []byte {
	extract := hmac.New(sha256.New, salt)
	_, _ = extract.Write(secret)
	prk := extract.Sum(nil)
	result := make([]byte, 0, length)
	var previous []byte
	for counter := byte(1); len(result) < length; counter++ {
		expand := hmac.New(sha256.New, prk)
		_, _ = expand.Write(previous)
		_, _ = expand.Write(info)
		_, _ = expand.Write([]byte{counter})
		previous = expand.Sum(nil)
		result = append(result, previous...)
	}
	return result[:length]
}

func decodeOperationResult(action string, status int, payload []byte) operationResult {
	result := operationResult{Action: action, Status: status}
	var body map[string]any
	if json.Unmarshal(payload, &body) == nil {
		result.Code, _ = body["code"].(string)
		result.Error, _ = body["error"].(string)
	}
	return result
}

func responseError(operation string, status int, payload []byte) error {
	message := strings.TrimSpace(string(payload))
	if len(message) > 500 {
		message = message[:500]
	}
	return fmt.Errorf("%s failed: status=%d body=%s", operation, status, message)
}

func recordStatuses(rep *report, name string, results []operationResult) {
	counts := make(map[string]int)
	for _, result := range results {
		counts[fmt.Sprintf("%d", result.Status)]++
	}
	rep.HTTPStatusCounts[name] = counts
}

func assertNoServerErrors(results []operationResult) {
	for _, result := range results {
		if result.Status == 0 || result.Status >= 500 {
			fatal(fmt.Errorf("%s returned unsafe result: status=%d error=%s", result.Action, result.Status, result.Error))
		}
	}
}

func manifestGeneration(raw json.RawMessage) int64 {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || bytes.Equal(raw, []byte(`""`)) {
		return 0
	}
	var direct struct {
		Generation int64 `json:"generation"`
	}
	if json.Unmarshal(raw, &direct) == nil && direct.Generation > 0 {
		return direct.Generation
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil && encoded != "" {
		_ = json.Unmarshal([]byte(encoded), &direct)
	}
	return direct.Generation
}

func manifestFirstChunkKey(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || bytes.Equal(raw, []byte(`""`)) {
		return ""
	}
	var direct struct {
		Chunks []struct {
			Key string `json:"key"`
		} `json:"chunks"`
	}
	if json.Unmarshal(raw, &direct) == nil && len(direct.Chunks) > 0 {
		return strings.TrimSpace(direct.Chunks[0].Key)
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil && encoded != "" {
		if json.Unmarshal([]byte(encoded), &direct) == nil && len(direct.Chunks) > 0 {
			return strings.TrimSpace(direct.Chunks[0].Key)
		}
	}
	return ""
}

func isStopped(state runtimeState) bool {
	return state.Status == "stopped" && state.DesiredState == "stopped" && state.ConnectionStatus == "offline"
}

func repeat(value string, count int) []string {
	result := make([]string, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func (c *qaClient) runtimeSuffix() string {
	return strings.ReplaceAll(c.accountID[:8], "-", "")
}

func fatal(err error) {
	panic(fatalFailure{err: err})
}

type fatalFailure struct {
	err error
}
