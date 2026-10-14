package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// App selections belong to a runtime: the catalog apps its node installs when
// they are missing. A snapshot carries installed apps and their data, so a
// selection decides what gets installed, not what is kept.

// ErrAppSelection marks a selection the catalog cannot satisfy.
var ErrAppSelection = errors.New("app selection is not valid")

const (
	// maxRuntimeAppSelections is how many selected apps a node installs.
	maxRuntimeAppSelections = 32
	// maxCatalogOffset bounds paging through the catalog.
	maxCatalogOffset = 100_000
)

// AppCatalogQuery asks for one page of the catalog. With AccountID, entries
// say whether they are selected: on RuntimeID when it is set, otherwise on
// any of the account's runtimes.
type AppCatalogQuery struct {
	AccountID string
	RuntimeID string
	Search    string
	// Limit defaults to, and is capped at, catalogResponseLimit.
	Limit  int
	Offset int
}

type AppCatalogPage struct {
	Items []AppCatalogEntry `json:"items"`
	// NextOffset is where the next page starts, or nil after the last one.
	NextOffset *int `json:"next_offset"`
}

func (s *Store) ListAppCatalogPage(ctx context.Context, query AppCatalogQuery) (AppCatalogPage, error) {
	search := strings.TrimSpace(strings.ToLower(query.Search))
	if len(search) > 80 {
		search = search[:80]
	}
	term := "%"
	if search != "" {
		term = "%" + search + "%"
	}
	limit := query.Limit
	if limit <= 0 || limit > catalogResponseLimit {
		limit = catalogResponseLimit
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > maxCatalogOffset {
		offset = maxCatalogOffset
	}

	// One row more than the page shows whether another page follows.
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.package_name, c.source, c.display_name, c.summary, c.icon_url,
		        c.version_name, c.version_code, c.apk_url, c.apk_sha256, c.apk_size_bytes,
		        c.min_sdk, c.native_code, c.license, c.categories_json, c.anti_features_json,
		        c.recommended, c.catalog_updated_at,
		        EXISTS (
		            SELECT 1
		              FROM runtime_app_selections s
		              JOIN runtimes r ON r.id = s.runtime_id
		             WHERE s.package_name = c.package_name
		               AND r.account_id::text = $1
		               AND r.deleted_at IS NULL
		               AND ($2 = '' OR r.id::text = $2)
		        ) AS selected
		   FROM app_catalog c
		  WHERE c.enabled = TRUE
		    AND ($3 = '%' OR LOWER(c.display_name) LIKE $3 OR LOWER(c.package_name) LIKE $3 OR LOWER(c.summary) LIKE $3)
		  ORDER BY c.recommended DESC, LOWER(c.display_name), c.package_name
		  LIMIT $4 OFFSET $5`,
		strings.TrimSpace(query.AccountID),
		strings.TrimSpace(query.RuntimeID),
		term,
		limit+1,
		offset,
	)
	if err != nil {
		return AppCatalogPage{}, err
	}
	defer rows.Close()
	entries, err := scanAppCatalogRows(rows)
	if err != nil {
		return AppCatalogPage{}, err
	}
	page := AppCatalogPage{Items: entries}
	if len(entries) > limit {
		page.Items = entries[:limit]
		next := offset + limit
		page.NextOffset = &next
	}
	if page.Items == nil {
		page.Items = []AppCatalogEntry{}
	}
	return page, nil
}

// RuntimeAppSelections lists the apps selected for one of the account's runtimes.
func (s *Store) RuntimeAppSelections(ctx context.Context, accountID, runtimeID string) ([]AppCatalogEntry, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (
		     SELECT 1 FROM runtimes
		      WHERE id::text = $1 AND account_id::text = $2 AND deleted_at IS NULL
		 )`,
		strings.TrimSpace(runtimeID),
		strings.TrimSpace(accountID),
	).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrRuntimeNotFound
	}
	return selectedAppsForRuntime(ctx, s.db, runtimeID)
}

// ReplaceRuntimeAppSelections sets the apps selected for one of the account's
// runtimes.
func (s *Store) ReplaceRuntimeAppSelections(ctx context.Context, accountID, runtimeID string, packageNames []string) ([]AppCatalogEntry, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var id string
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM runtimes
		  WHERE id::text = $1 AND account_id::text = $2 AND deleted_at IS NULL
		  FOR UPDATE`,
		strings.TrimSpace(runtimeID),
		strings.TrimSpace(accountID),
	).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRuntimeNotFound
		}
		return nil, err
	}
	if err := replaceRuntimeAppSelectionsTX(ctx, tx, id, packageNames); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return selectedAppsForRuntime(ctx, s.db, id)
}

// ReplaceAccountAppSelections serves app releases from before selections
// moved to runtimes: it sets the same apps on every runtime of the account.
func (s *Store) ReplaceAccountAppSelections(ctx context.Context, accountID string, packageNames []string) ([]AppCatalogEntry, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, errors.New("account id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	normalized, err := validRuntimeAppSelectionsTX(ctx, tx, packageNames)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM runtimes WHERE account_id::text = $1 AND deleted_at IS NULL FOR UPDATE`,
		accountID,
	)
	if err != nil {
		return nil, err
	}
	var runtimeIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		runtimeIDs = append(runtimeIDs, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, id := range runtimeIDs {
		if err := writeRuntimeAppSelectionsTX(ctx, tx, id, normalized); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	page, err := s.ListAppCatalogPage(ctx, AppCatalogQuery{AccountID: accountID})
	return page.Items, err
}

func replaceRuntimeAppSelectionsTX(ctx context.Context, tx *sql.Tx, runtimeID string, packageNames []string) error {
	normalized, err := validRuntimeAppSelectionsTX(ctx, tx, packageNames)
	if err != nil {
		return err
	}
	return writeRuntimeAppSelectionsTX(ctx, tx, runtimeID, normalized)
}

// validRuntimeAppSelectionsTX normalizes package names and checks each is an
// enabled catalog app.
func validRuntimeAppSelectionsTX(ctx context.Context, tx *sql.Tx, packageNames []string) ([]string, error) {
	normalized, err := normalizeAppPackageSelections(packageNames)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAppSelection, err)
	}
	if len(normalized) > maxRuntimeAppSelections {
		return nil, fmt.Errorf("%w: select at most %d apps", ErrAppSelection, maxRuntimeAppSelections)
	}
	for _, packageName := range normalized {
		var exists bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (
			    SELECT 1 FROM app_catalog
			     WHERE package_name = $1
			       AND enabled = TRUE
			)`,
			packageName,
		).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("%w: app package is not available: %s", ErrAppSelection, packageName)
		}
	}
	return normalized, nil
}

// writeRuntimeAppSelectionsTX keeps when each still-selected app was first
// selected: a node reinstalls an app the user removed only for a newer one.
func writeRuntimeAppSelectionsTX(ctx context.Context, tx *sql.Tx, runtimeID string, packageNames []string) error {
	if packageNames == nil {
		packageNames = []string{} // NULL would match nothing and keep every row
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM runtime_app_selections WHERE runtime_id = $1 AND NOT (package_name = ANY($2))`,
		runtimeID,
		packageNames,
	); err != nil {
		return err
	}
	for _, packageName := range packageNames {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO runtime_app_selections (runtime_id, package_name, selected_at)
			 VALUES ($1, $2, NOW())
			 ON CONFLICT (runtime_id, package_name) DO NOTHING`,
			runtimeID,
			packageName,
		); err != nil {
			return err
		}
	}
	return nil
}

func selectedAppsForRuntime(ctx context.Context, q queryContexter, runtimeID string) ([]AppCatalogEntry, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT c.package_name, c.source, c.display_name, c.summary, c.icon_url,
		        c.version_name, c.version_code, c.apk_url, c.apk_sha256, c.apk_size_bytes,
		        c.min_sdk, c.native_code, c.license, c.categories_json, c.anti_features_json,
		        c.recommended, c.catalog_updated_at, s.selected_at
		   FROM runtime_app_selections s
		   JOIN app_catalog c ON c.package_name = s.package_name
		  WHERE s.runtime_id = $1
		    AND c.enabled = TRUE
		  ORDER BY c.recommended DESC, LOWER(c.display_name), c.package_name
		  LIMIT $2`,
		runtimeID,
		maxRuntimeAppSelections,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []AppCatalogEntry{}
	for rows.Next() {
		var entry AppCatalogEntry
		var selectedAt time.Time
		if err := rows.Scan(
			&entry.PackageName, &entry.Source, &entry.DisplayName, &entry.Summary, &entry.IconURL,
			&entry.VersionName, &entry.VersionCode, &entry.APKURL, &entry.APKSHA256, &entry.APKSizeBytes,
			&entry.MinSDK, &entry.NativeCode, &entry.License, &entry.CategoriesJSON, &entry.AntiFeaturesJSON,
			&entry.Recommended, &entry.CatalogUpdatedAt, &selectedAt,
		); err != nil {
			return nil, err
		}
		entry.Selected = true
		entry.SelectedAt = &selectedAt
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func attachSelectedAppsToRuntimesTX(ctx context.Context, tx *sql.Tx, runtimes []Runtime) error {
	for i := range runtimes {
		apps, err := selectedAppsForRuntime(ctx, tx, runtimes[i].ID)
		if err != nil {
			return err
		}
		runtimes[i].SelectedApps = apps
	}
	return nil
}
