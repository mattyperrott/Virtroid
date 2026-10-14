package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"virtroid/backend/internal/nodeauth"
)

// Provisioned runtime VMs: one dedicated runtime node per account, created and
// deleted on Aleph Cloud by the provisioner service. The control plane decides
// which VMs should exist; the provisioner reports what it did.

const (
	ProvisionedVMRequested = "requested"
	ProvisionedVMCreating  = "creating"
	ProvisionedVMUnlocking = "unlocking"
	ProvisionedVMBooting   = "booting"
	ProvisionedVMReady     = "ready"
	ProvisionedVMDeleted   = "deleted"

	// ProvisionerOperatorID owns the node approvals of provisioned VMs.
	ProvisionerOperatorID = "aleph-provisioner"

	RuntimeHostProvisioningCode = "runtime_host_provisioning"
	ProvisioningCapacityCode    = "runtime_host_capacity"

	// Serializes VM requests, spare top-ups, and the capacity check.
	provisioningLockKey        int64 = 0x5669727450564d73
	maxProvisionerFailureRunes       = 1024
)

var (
	ErrProvisioningCapacity    = errors.New("no runtime VM capacity is available")
	ErrProvisionedVMNotFound   = errors.New("provisioned VM not found")
	ErrProvisionedVMTransition = errors.New("provisioned VM cannot move to that state")
)

type ProvisionedVM struct {
	ID             string     `json:"id"`
	AccountID      *string    `json:"account_id,omitempty"`
	Region         string     `json:"region"`
	State          string     `json:"state"`
	Desired        string     `json:"desired"`
	ItemHash       string     `json:"item_hash,omitempty"`
	CRNHash        string     `json:"crn_hash,omitempty"`
	PublicIP       string     `json:"public_ip,omitempty"`
	NodePublicKey  string     `json:"node_public_key,omitempty"`
	Failure        string     `json:"failure,omitempty"`
	RetireReason   string     `json:"retire_reason,omitempty"`
	Progress       string     `json:"progress,omitempty"`
	RequestedAt    time.Time  `json:"requested_at"`
	StateChangedAt time.Time  `json:"state_changed_at"`
	ReadyAt        *time.Time `json:"ready_at,omitempty"`
	LastUsedAt     time.Time  `json:"last_used_at"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
}

// ProvisioningPolicy bounds what the control plane asks the provisioner for.
type ProvisioningPolicy struct {
	Regions      []string
	MaxVMs       int
	WarmSpares   int
	SetupTimeout time.Duration
	RetireAfter  time.Duration
}

// ProvisionerReport is what the provisioner learned or did for one VM. State
// is the new state, or empty when the report only carries a failure.
type ProvisionerReport struct {
	State         string
	ItemHash      string
	CRNHash       string
	PublicIP      string
	NodePublicKey string
	Failure       string
	// Progress is a short note on what the VM is doing while it comes up.
	Progress string
}

type ProvisioningSummary struct {
	TimedOut      int
	AccountsGone  int
	Retired       int
	SparesCreated int
	// RuntimesReset counts runtimes whose VM was deleted with their data.
	RuntimesReset int
}

// lostRuntimeDataMessage is the runtime log entry for a runtime whose VM, and
// the snapshot on its data disk, is gone.
const lostRuntimeDataMessage = "This runtime's private server was replaced, and the data stored on it could not be kept. It starts fresh next time."

const provisionedVMColumns = `id, account_id::text, region, state, desired, item_hash, crn_hash,
	public_ip, node_public_key, failure, retire_reason, progress, requested_at, state_changed_at,
	ready_at, last_used_at, deleted_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProvisionedVM(row rowScanner) (ProvisionedVM, error) {
	var vm ProvisionedVM
	var accountID sql.NullString
	var readyAt, deletedAt sql.NullTime
	if err := row.Scan(
		&vm.ID,
		&accountID,
		&vm.Region,
		&vm.State,
		&vm.Desired,
		&vm.ItemHash,
		&vm.CRNHash,
		&vm.PublicIP,
		&vm.NodePublicKey,
		&vm.Failure,
		&vm.RetireReason,
		&vm.Progress,
		&vm.RequestedAt,
		&vm.StateChangedAt,
		&readyAt,
		&vm.LastUsedAt,
		&deletedAt,
	); err != nil {
		return ProvisionedVM{}, err
	}
	if accountID.Valid {
		vm.AccountID = &accountID.String
	}
	if readyAt.Valid {
		vm.ReadyAt = &readyAt.Time
	}
	if deletedAt.Valid {
		vm.DeletedAt = &deletedAt.Time
	}
	return vm, nil
}

func newProvisionedVMID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "pvm-" + hex.EncodeToString(raw), nil
}

// RegionIsProvisioned reports whether the provisioner serves a region.
func (p ProvisioningPolicy) RegionIsProvisioned(region string) bool {
	region = strings.ToLower(strings.TrimSpace(region))
	if region == "" || p.MaxVMs <= 0 {
		return false
	}
	for _, candidate := range p.Regions {
		if strings.EqualFold(strings.TrimSpace(candidate), region) {
			return true
		}
	}
	return false
}

// AccountProvisionedRegion returns the account's region when the provisioner
// serves it, or "" when the account's runtimes run on shared nodes.
func (s *Store) AccountProvisionedRegion(ctx context.Context, accountID string, policy ProvisioningPolicy) (string, error) {
	var region string
	err := s.db.QueryRowContext(ctx,
		`SELECT runtime_region FROM accounts WHERE id::text = $1 AND deleted_at IS NULL`,
		strings.TrimSpace(accountID),
	).Scan(&region)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrAccountNotFound
	}
	if err != nil {
		return "", err
	}
	if !policy.RegionIsProvisioned(region) {
		return "", nil
	}
	return region, nil
}

// EnsureAccountVM returns the account's runtime VM, claiming a warm spare or
// requesting a new VM when it has none. It never exceeds policy.MaxVMs.
func (s *Store) EnsureAccountVM(ctx context.Context, accountID, region string, policy ProvisioningPolicy) (ProvisionedVM, error) {
	accountID = strings.TrimSpace(accountID)
	region = strings.ToLower(strings.TrimSpace(region))
	if !policy.RegionIsProvisioned(region) {
		return ProvisionedVM{}, fmt.Errorf("region %q is not provisioned", region)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProvisionedVM{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, provisioningLockKey); err != nil {
		return ProvisionedVM{}, err
	}

	vm, err := scanProvisionedVM(tx.QueryRowContext(ctx,
		`UPDATE provisioned_vms SET last_used_at = NOW()
		  WHERE account_id::text = $1 AND desired = 'present'
		  RETURNING `+provisionedVMColumns,
		accountID,
	))
	if err == nil {
		return vm, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ProvisionedVM{}, err
	}

	// A spare that is already running reaches the user soonest.
	vm, err = scanProvisionedVM(tx.QueryRowContext(ctx,
		`UPDATE provisioned_vms SET account_id = $1::uuid, last_used_at = NOW()
		  WHERE id = (
		      SELECT id FROM provisioned_vms
		       WHERE account_id IS NULL AND desired = 'present' AND state <> 'deleted' AND region = $2
		       ORDER BY (state = 'ready') DESC, requested_at
		       LIMIT 1
		  )
		  RETURNING `+provisionedVMColumns,
		accountID,
		region,
	))
	if err == nil {
		return vm, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ProvisionedVM{}, err
	}

	var live int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM provisioned_vms WHERE state <> 'deleted'`).Scan(&live); err != nil {
		return ProvisionedVM{}, err
	}
	if live >= policy.MaxVMs {
		return ProvisionedVM{}, ErrProvisioningCapacity
	}
	id, err := newProvisionedVMID()
	if err != nil {
		return ProvisionedVM{}, err
	}
	vm, err = scanProvisionedVM(tx.QueryRowContext(ctx,
		`INSERT INTO provisioned_vms (id, account_id, region)
		 VALUES ($1, $2::uuid, $3)
		 RETURNING `+provisionedVMColumns,
		id,
		accountID,
		region,
	))
	if err != nil {
		return ProvisionedVM{}, err
	}
	return vm, tx.Commit()
}

// ProvisionerWork lists every VM the provisioner still has to act on or
// watch: everything that is not yet deleted.
func (s *Store) ProvisionerWork(ctx context.Context) ([]ProvisionedVM, error) {
	return s.queryProvisionedVMs(ctx,
		`SELECT `+provisionedVMColumns+` FROM provisioned_vms
		  WHERE state <> 'deleted'
		  ORDER BY requested_at`)
}

// ListProvisionedVMs lists VMs for operators, newest first.
func (s *Store) ListProvisionedVMs(ctx context.Context, includeDeleted bool) ([]ProvisionedVM, error) {
	return s.queryProvisionedVMs(ctx,
		`SELECT `+provisionedVMColumns+` FROM provisioned_vms
		  WHERE $1 OR state <> 'deleted'
		  ORDER BY requested_at DESC`,
		includeDeleted)
}

func (s *Store) queryProvisionedVMs(ctx context.Context, query string, args ...any) ([]ProvisionedVM, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var vms []ProvisionedVM
	for rows.Next() {
		vm, err := scanProvisionedVM(rows)
		if err != nil {
			return nil, err
		}
		vms = append(vms, vm)
	}
	return vms, rows.Err()
}

func (s *Store) ProvisionedVM(ctx context.Context, id string) (ProvisionedVM, error) {
	vm, err := scanProvisionedVM(s.db.QueryRowContext(ctx,
		`SELECT `+provisionedVMColumns+` FROM provisioned_vms WHERE id = $1`,
		strings.TrimSpace(id),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return ProvisionedVM{}, ErrProvisionedVMNotFound
	}
	return vm, err
}

// LiveProvisionedVM returns the provisioned VM behind a node id when it is
// not deleted. ok is false for nodes that are not provisioned VMs.
func (s *Store) LiveProvisionedVM(ctx context.Context, nodeID string) (ProvisionedVM, bool, error) {
	vm, err := scanProvisionedVM(s.db.QueryRowContext(ctx,
		`SELECT `+provisionedVMColumns+` FROM provisioned_vms WHERE id = $1 AND state <> 'deleted'`,
		strings.TrimSpace(nodeID),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return ProvisionedVM{}, false, nil
	}
	if err != nil {
		return ProvisionedVM{}, false, err
	}
	return vm, true, nil
}

// RetireProvisionedVM asks the provisioner to delete a VM.
func (s *Store) RetireProvisionedVM(ctx context.Context, id, reason string) (ProvisionedVM, error) {
	vm, err := scanProvisionedVM(s.db.QueryRowContext(ctx,
		`UPDATE provisioned_vms
		    SET desired = 'absent',
		        retire_reason = CASE WHEN desired = 'present' THEN $2 ELSE retire_reason END
		  WHERE id = $1
		  RETURNING `+provisionedVMColumns,
		strings.TrimSpace(id),
		boundedRegistryText(reason, 256),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return ProvisionedVM{}, ErrProvisionedVMNotFound
	}
	return vm, err
}

// ApplyProvisionerReport records a step the provisioner finished. A VM's node
// key is approved when it reaches unlocking, before the key is handed to the
// VM, and revoked when the VM is deleted.
func (s *Store) ApplyProvisionerReport(ctx context.Context, id string, report ProvisionerReport) (ProvisionedVM, error) {
	id = strings.TrimSpace(id)
	report.State = strings.TrimSpace(report.State)
	current, err := s.ProvisionedVM(ctx, id)
	if err != nil {
		return ProvisionedVM{}, err
	}
	if current.State == ProvisionedVMDeleted {
		return current, nil
	}

	var nodePublicKey string
	switch report.State {
	case "", ProvisionedVMDeleted:
	case ProvisionedVMCreating:
		if !validItemHash(report.ItemHash) {
			return ProvisionedVM{}, errors.New("item_hash must be a 64-character hex item hash")
		}
		if report.CRNHash != "" && !validItemHash(report.CRNHash) {
			return ProvisionedVM{}, errors.New("crn_hash must be a 64-character hex node hash")
		}
	case ProvisionedVMUnlocking:
		ip := net.ParseIP(strings.TrimSpace(report.PublicIP))
		if ip == nil || !ip.IsGlobalUnicast() {
			return ProvisionedVM{}, errors.New("public_ip must be a global unicast IP address")
		}
		report.PublicIP = ip.String()
		nodePublicKey, _, err = nodeauth.NormalizePublicKey(report.NodePublicKey)
		if err != nil {
			return ProvisionedVM{}, fmt.Errorf("node_public_key: %w", err)
		}
		if current.NodePublicKey != "" && current.NodePublicKey != nodePublicKey {
			return ProvisionedVM{}, errors.New("a provisioned VM's node key cannot change")
		}
		if current.Desired != "present" {
			return ProvisionedVM{}, ErrProvisionedVMTransition
		}
		if _, err := s.ApproveNode(ctx, ApproveNodeInput{
			NodeID:       id,
			OperatorID:   ProvisionerOperatorID,
			OperatorName: "Aleph Cloud provisioner",
			PublicKey:    nodePublicKey,
			Actor:        "provisioner",
			Reason:       "provisioned runtime VM " + id,
		}); err != nil {
			return ProvisionedVM{}, err
		}
	case ProvisionedVMBooting:
	default:
		return ProvisionedVM{}, ErrProvisionedVMTransition
	}

	allowedFrom := map[string][]string{
		ProvisionedVMCreating:  {ProvisionedVMRequested, ProvisionedVMCreating},
		ProvisionedVMUnlocking: {ProvisionedVMCreating, ProvisionedVMUnlocking},
		ProvisionedVMBooting:   {ProvisionedVMUnlocking, ProvisionedVMBooting},
	}[report.State]
	if allowedFrom != nil && !containsString(allowedFrom, current.State) {
		return ProvisionedVM{}, ErrProvisionedVMTransition
	}

	failure := boundedRegistryText(report.Failure, maxProvisionerFailureRunes)
	progress := boundedRegistryText(report.Progress, 256)
	vm, err := scanProvisionedVM(s.db.QueryRowContext(ctx,
		`UPDATE provisioned_vms
		    SET state = CASE WHEN $2 = '' THEN state ELSE $2 END,
		        state_changed_at = CASE WHEN $2 = '' OR $2 = state THEN state_changed_at ELSE NOW() END,
		        item_hash = CASE WHEN $3 = '' THEN item_hash ELSE $3 END,
		        crn_hash = CASE WHEN $4 = '' THEN crn_hash ELSE $4 END,
		        public_ip = CASE WHEN $5 = '' THEN public_ip ELSE $5 END,
		        node_public_key = CASE WHEN $6 = '' THEN node_public_key ELSE $6 END,
		        failure = CASE WHEN $7 = '' THEN failure ELSE $7 END,
		        desired = CASE WHEN $7 = '' THEN desired ELSE 'absent' END,
		        retire_reason = CASE WHEN $7 <> '' AND desired = 'present' THEN 'failed' ELSE retire_reason END,
		        deleted_at = CASE WHEN $2 = 'deleted' THEN NOW() ELSE deleted_at END,
		        progress = CASE WHEN $8 <> '' THEN $8 WHEN $2 <> '' AND $2 <> state THEN '' ELSE progress END
		  WHERE id = $1 AND state <> 'deleted'
		  RETURNING `+provisionedVMColumns,
		id,
		report.State,
		strings.ToLower(strings.TrimSpace(report.ItemHash)),
		strings.ToLower(strings.TrimSpace(report.CRNHash)),
		report.PublicIP,
		nodePublicKey,
		failure,
		progress,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return s.ProvisionedVM(ctx, id)
	}
	if err != nil {
		return ProvisionedVM{}, err
	}
	if report.State == ProvisionedVMDeleted && vm.NodePublicKey != "" {
		if _, err := s.RevokeNode(ctx, RevokeNodeInput{
			NodeID: id,
			Actor:  "provisioner",
			Reason: "provisioned runtime VM deleted",
		}); err != nil && !errors.Is(err, ErrApprovedNodeNotFound) && !errors.Is(err, ErrApprovedNodeRevoked) {
			return ProvisionedVM{}, err
		}
	}
	return vm, nil
}

// MarkProvisionedVMReady records the first heartbeat from a VM's node.
func (s *Store) MarkProvisionedVMReady(ctx context.Context, nodeID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE provisioned_vms
		    SET state = 'ready', state_changed_at = NOW(), ready_at = COALESCE(ready_at, NOW())
		  WHERE id = $1 AND state IN ('unlocking', 'booting')`,
		strings.TrimSpace(nodeID),
	)
	return err
}

// ReconcileProvisionedVMs applies the provisioning policy: it gives up on VMs
// that never finished setting up, retires VMs whose account is gone or that
// have held no runtime for policy.RetireAfter, and tops up warm spares.
func (s *Store) ReconcileProvisionedVMs(ctx context.Context, policy ProvisioningPolicy) (ProvisioningSummary, error) {
	var summary ProvisioningSummary
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return summary, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, provisioningLockKey); err != nil {
		return summary, err
	}

	setupTimeout := max(policy.SetupTimeout, time.Minute)
	result, err := tx.ExecContext(ctx,
		`UPDATE provisioned_vms
		    SET desired = 'absent',
		        retire_reason = 'setup-timeout',
		        failure = CASE WHEN failure = '' THEN 'setup did not finish in time' ELSE failure END
		  WHERE desired = 'present'
		    AND state IN ('requested', 'creating', 'unlocking', 'booting')
		    AND requested_at < NOW() - make_interval(secs => $1)`,
		setupTimeout.Seconds(),
	)
	if err != nil {
		return summary, err
	}
	summary.TimedOut = rowsAffected(result)

	if _, err := tx.ExecContext(ctx,
		`UPDATE provisioned_vms p SET last_used_at = NOW()
		  WHERE p.state <> 'deleted' AND p.desired = 'present'
		    AND EXISTS (
		        SELECT 1 FROM runtimes r
		         WHERE r.deleted_at IS NULL AND (r.host_id = p.id OR r.blob_host_id = p.id)
		    )`,
	); err != nil {
		return summary, err
	}

	result, err = tx.ExecContext(ctx,
		`UPDATE provisioned_vms p
		    SET desired = 'absent', retire_reason = 'account-deleted'
		  WHERE p.desired = 'present' AND p.account_id IS NOT NULL
		    AND NOT EXISTS (SELECT 1 FROM accounts a WHERE a.id = p.account_id AND a.deleted_at IS NULL)`,
	)
	if err != nil {
		return summary, err
	}
	summary.AccountsGone = rowsAffected(result)

	if policy.RetireAfter > 0 {
		result, err = tx.ExecContext(ctx,
			`UPDATE provisioned_vms p
			    SET desired = 'absent', retire_reason = 'unused'
			  WHERE p.desired = 'present' AND p.account_id IS NOT NULL AND p.state = 'ready'
			    AND p.last_used_at < NOW() - make_interval(secs => $1)
			    AND NOT EXISTS (
			        SELECT 1 FROM runtimes r
			         WHERE r.deleted_at IS NULL AND (r.host_id = p.id OR r.blob_host_id = p.id)
			    )`,
			policy.RetireAfter.Seconds(),
		)
		if err != nil {
			return summary, err
		}
		summary.Retired = rowsAffected(result)
	}

	// A deleted VM took the snapshots on its data disk with it. Runtimes that
	// lived there would otherwise wait for it forever: forget the lost data,
	// say so in the runtime's log, and let it start fresh on the account's
	// next VM. The floor counts the lost generations, so phones still see
	// floor + generation rise when its snapshots are numbered from 1 again. Deletions waiting on that VM have nothing left to clean.
	// A snapshot in a remote store such as Sia is not lost: the runtime keeps
	// it and starts from it on the next VM, and a deletion waits for the next
	// node of the account to remove it.
	rows, err := tx.QueryContext(ctx,
		`WITH affected AS (
		     SELECT id,
		            NULLIF(TRIM(COALESCE(blob_manifest_json, '')), '') IS NOT NULL
		            AND COALESCE(NULLIF(TRIM(blob_store_kind), ''), 'local-disk') <> 'local-disk' AS remote,
		            `+manifestGenerationSQL+` AS lost_generation
		       FROM runtimes
		      WHERE deleted_at IS NULL
		        AND (host_id IN (SELECT id FROM provisioned_vms WHERE state = 'deleted')
		             OR blob_host_id IN (SELECT id FROM provisioned_vms WHERE state = 'deleted'))
		 )
		 UPDATE runtimes r
		    SET host_id = NULL,
		        blob_host_id = NULL,
		        blob_manifest_json = CASE WHEN affected.remote THEN r.blob_manifest_json ELSE NULL END,
		        blob_generation_floor = CASE WHEN affected.remote THEN r.blob_generation_floor
		            ELSE r.blob_generation_floor + affected.lost_generation END,
		        blob_last_snapshot_at = CASE WHEN affected.remote THEN r.blob_last_snapshot_at ELSE NULL END,
		        cleanup_pending = FALSE,
		        container_name = NULL,
		        adb_port = NULL,
		        viewer_port = CASE WHEN r.desired_state = 'deleted' AND NOT affected.remote THEN NULL ELSE r.viewer_port END,
		        started_at = NULL,
		        connection_status = 'offline',
		        status = CASE
		            WHEN r.desired_state = 'deleted' AND affected.remote THEN 'deleting'
		            WHEN r.desired_state = 'deleted' THEN 'deleted'
		            ELSE 'stopped'
		        END,
		        desired_state = CASE WHEN r.desired_state = 'deleted' THEN 'deleted' ELSE 'stopped' END,
		        deleted_at = CASE WHEN r.desired_state = 'deleted' AND NOT affected.remote THEN NOW() ELSE NULL END,
		        operation_generation = r.operation_generation + 1,
		        updated_at = NOW()
		   FROM affected
		  WHERE r.id = affected.id
		  RETURNING r.id, r.desired_state, affected.remote`,
	)
	if err != nil {
		return summary, err
	}
	var lost []string
	for rows.Next() {
		var runtimeID, desired string
		var remote bool
		if err := rows.Scan(&runtimeID, &desired, &remote); err != nil {
			rows.Close()
			return summary, err
		}
		summary.RuntimesReset++
		if desired != "deleted" && !remote {
			lost = append(lost, runtimeID)
		}
	}
	if err := rows.Close(); err != nil {
		return summary, err
	}
	if err := rows.Err(); err != nil {
		return summary, err
	}
	for _, runtimeID := range lost {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO runtime_logs (runtime_id, source, level, message) VALUES ($1, 'control-plane', 'warn', $2)`,
			runtimeID,
			lostRuntimeDataMessage,
		); err != nil {
			return summary, err
		}
	}

	if policy.MaxVMs > 0 && policy.WarmSpares > 0 {
		var live int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM provisioned_vms WHERE state <> 'deleted'`).Scan(&live); err != nil {
			return summary, err
		}
		for _, region := range policy.Regions {
			region = strings.ToLower(strings.TrimSpace(region))
			if !ValidRegion(region) || region == "" {
				continue
			}
			var spares int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM provisioned_vms
				  WHERE account_id IS NULL AND desired = 'present' AND state <> 'deleted' AND region = $1`,
				region,
			).Scan(&spares); err != nil {
				return summary, err
			}
			for ; spares < policy.WarmSpares && live < policy.MaxVMs; spares, live = spares+1, live+1 {
				id, err := newProvisionedVMID()
				if err != nil {
					return summary, err
				}
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO provisioned_vms (id, region) VALUES ($1, $2)`,
					id,
					region,
				); err != nil {
					return summary, err
				}
				summary.SparesCreated++
			}
		}
	}
	return summary, tx.Commit()
}

func rowsAffected(result sql.Result) int {
	count, err := result.RowsAffected()
	if err != nil {
		return 0
	}
	return int(count)
}

func validItemHash(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// provisionedHostServesAccount is a placement condition: a host that is a
// provisioned VM only takes runtimes of the account it belongs to, and only
// while that VM is meant to exist. Shared hosts are unaffected.
func provisionedHostServesAccount(hostColumn, accountParam string) string {
	return `NOT EXISTS (
		       SELECT 1 FROM provisioned_vms dedicated
		        WHERE dedicated.id = ` + hostColumn + `
		          AND (dedicated.account_id IS NULL
		               OR dedicated.account_id::text <> ` + accountParam + `
		               OR dedicated.desired <> 'present')
		   )`
}

// SetHostSNPAttestation records the attestation bundle a node sent with its
// heartbeat ("" when it has none).
func (s *Store) SetHostSNPAttestation(ctx context.Context, hostID, attestation string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE hosts SET snp_attestation = $2 WHERE id = $1 AND snp_attestation IS DISTINCT FROM $2`,
		strings.TrimSpace(hostID),
		attestation,
	)
	return err
}

// HostSNPAttestation returns a node's attestation bundle, or "" when it has none.
func (s *Store) HostSNPAttestation(ctx context.Context, hostID string) (string, error) {
	var attestation string
	err := s.db.QueryRowContext(ctx,
		`SELECT snp_attestation FROM hosts WHERE id = $1`,
		strings.TrimSpace(hostID),
	).Scan(&attestation)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return attestation, err
}
