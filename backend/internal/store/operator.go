package store

import (
	"context"
	"database/sql"
	"time"
)

// OperatorSnapshot is deliberately narrower than the underlying data model.
// It excludes credentials, relay tokens, blob manifests, network addresses,
// raw security-event output, and other secrets that do not belong in a UI.
type OperatorSnapshot struct {
	GeneratedAt      time.Time
	ActiveAccounts   int
	TotalRuntimes    int
	RunningRuntimes  int
	LiveSessions     int
	CleanupPending   int
	ExpiredSessions  int
	OrphanedRuntimes int
	Readiness        NodeReadiness
	Hosts            []OperatorHost
	Runtimes         []OperatorRuntime
	Incidents        []OperatorIncident
	Activity         []OperatorActivityBucket
}

type OperatorHost struct {
	ID              string
	Name            string
	LastHeartbeatAt time.Time
}

type OperatorRuntime struct {
	ID                 string
	AccountID          string
	Name               string
	Status             string
	DesiredState       string
	ConnectionStatus   string
	HostID             string
	PersonaVersion     int
	AndroidVersion     string
	WidthPx            int
	HeightPx           int
	DensityDPI         int
	BlobStoreKind      string
	BlobLastSnapshotAt *time.Time
	StartedAt          *time.Time
	LoadAverage        *float64
	CleanupPending     bool
	LastError          *string
	UpdatedAt          time.Time
	ActiveSessionAt    *time.Time
}

type OperatorIncident struct {
	ID        int64
	NodeID    string
	Source    string
	Rule      string
	Priority  string
	EventTime time.Time
}

type OperatorActivityBucket struct {
	Bucket     time.Time
	Sessions   int
	Operations int
}

func (s *Store) OperatorSnapshot(ctx context.Context) (OperatorSnapshot, error) {
	var out OperatorSnapshot
	out.GeneratedAt = time.Now().UTC()
	if s == nil || s.db == nil {
		return out, sql.ErrConnDone
	}

	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM accounts WHERE deleted_at IS NULL),
			(SELECT COUNT(*) FROM runtimes WHERE deleted_at IS NULL),
			(SELECT COUNT(*) FROM runtimes WHERE deleted_at IS NULL AND status = 'running'),
			(SELECT COUNT(*) FROM sessions WHERE status IN ('pending', 'active') AND expires_at > NOW()),
			(SELECT COUNT(*) FROM runtimes WHERE cleanup_pending),
			(SELECT COUNT(*) FROM sessions WHERE status IN ('pending', 'active') AND expires_at <= NOW()),
			(SELECT COUNT(*) FROM runtimes r LEFT JOIN accounts a ON a.id = r.account_id WHERE a.id IS NULL)
	`).Scan(&out.ActiveAccounts, &out.TotalRuntimes, &out.RunningRuntimes, &out.LiveSessions, &out.CleanupPending, &out.ExpiredSessions, &out.OrphanedRuntimes)
	if err != nil {
		return out, err
	}

	readiness, err := s.NodeReadiness(ctx)
	if err != nil {
		return out, err
	}
	out.Readiness = readiness

	hostRows, err := s.db.QueryContext(ctx, `SELECT id, name, last_heartbeat_at FROM hosts ORDER BY last_heartbeat_at DESC LIMIT 20`)
	if err != nil {
		return out, err
	}
	defer hostRows.Close()
	for hostRows.Next() {
		var host OperatorHost
		if err := hostRows.Scan(&host.ID, &host.Name, &host.LastHeartbeatAt); err != nil {
			return out, err
		}
		out.Hosts = append(out.Hosts, host)
	}
	if err := hostRows.Err(); err != nil {
		return out, err
	}

	runtimeRows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.account_id, r.name, r.status, r.desired_state, r.connection_status,
		       COALESCE(r.host_id, ''), r.persona_version, r.android_version,
		       r.width_px, r.height_px, r.density_dpi, COALESCE(r.blob_store_kind, 'local-disk'),
		       r.blob_last_snapshot_at, r.started_at, r.load_average, r.cleanup_pending,
		       NULLIF(BTRIM(COALESCE(r.last_error, '')), ''), r.updated_at,
		       (SELECT COALESCE(s.last_client_heartbeat_at, s.updated_at, s.created_at)
		          FROM sessions s
		         WHERE s.runtime_id = r.id AND s.status IN ('pending', 'active') AND s.expires_at > NOW()
		         ORDER BY COALESCE(s.last_client_heartbeat_at, s.updated_at, s.created_at) DESC LIMIT 1)
		  FROM runtimes r
		 WHERE r.deleted_at IS NULL
		 ORDER BY r.updated_at DESC
		 LIMIT 25
	`)
	if err != nil {
		return out, err
	}
	defer runtimeRows.Close()
	for runtimeRows.Next() {
		var runtime OperatorRuntime
		if err := runtimeRows.Scan(
			&runtime.ID, &runtime.AccountID, &runtime.Name, &runtime.Status, &runtime.DesiredState,
			&runtime.ConnectionStatus, &runtime.HostID, &runtime.PersonaVersion, &runtime.AndroidVersion,
			&runtime.WidthPx, &runtime.HeightPx, &runtime.DensityDPI, &runtime.BlobStoreKind,
			&runtime.BlobLastSnapshotAt, &runtime.StartedAt, &runtime.LoadAverage,
			&runtime.CleanupPending, &runtime.LastError, &runtime.UpdatedAt, &runtime.ActiveSessionAt,
		); err != nil {
			return out, err
		}
		out.Runtimes = append(out.Runtimes, runtime)
	}
	if err := runtimeRows.Err(); err != nil {
		return out, err
	}

	incidentRows, err := s.db.QueryContext(ctx, `
		SELECT id, node_id, source, rule, priority, COALESCE(event_time, created_at)
		  FROM security_events
		 ORDER BY COALESCE(event_time, created_at) DESC
		 LIMIT 12
	`)
	if err != nil {
		return out, err
	}
	defer incidentRows.Close()
	for incidentRows.Next() {
		var incident OperatorIncident
		if err := incidentRows.Scan(&incident.ID, &incident.NodeID, &incident.Source, &incident.Rule, &incident.Priority, &incident.EventTime); err != nil {
			return out, err
		}
		out.Incidents = append(out.Incidents, incident)
	}
	if err := incidentRows.Err(); err != nil {
		return out, err
	}

	activityRows, err := s.db.QueryContext(ctx, `
		WITH buckets AS (
			SELECT generate_series(
				date_trunc('hour', NOW()) - INTERVAL '20 hours',
				date_trunc('hour', NOW()),
				INTERVAL '4 hours'
			) AS bucket
		)
		SELECT b.bucket,
		       (SELECT COUNT(*) FROM sessions s WHERE s.created_at >= b.bucket AND s.created_at < b.bucket + INTERVAL '4 hours'),
		       (SELECT COUNT(*) FROM runtime_start_events e WHERE e.created_at >= b.bucket AND e.created_at < b.bucket + INTERVAL '4 hours')
		  FROM buckets b ORDER BY b.bucket
	`)
	if err != nil {
		return out, err
	}
	defer activityRows.Close()
	for activityRows.Next() {
		var bucket OperatorActivityBucket
		if err := activityRows.Scan(&bucket.Bucket, &bucket.Sessions, &bucket.Operations); err != nil {
			return out, err
		}
		out.Activity = append(out.Activity, bucket)
	}
	return out, activityRows.Err()
}
