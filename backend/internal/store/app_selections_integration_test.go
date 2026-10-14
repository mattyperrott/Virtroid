package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPostgresRuntimeAppSelectionsIntegration(t *testing.T) {
	databaseURL := os.Getenv("VIRTROID_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VIRTROID_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	accountID := uuid.NewString()
	if _, err := st.BootstrapAccountWithIdentity(
		ctx, accountID, uuid.NewString(), "App selections QA", integrationNodePublicKey(t), CreateRuntimeInput{},
	); err != nil {
		t.Fatalf("bootstrap account: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteAccount(context.Background(), accountID) })
	if _, err := st.db.ExecContext(ctx, `UPDATE account_entitlements SET runtime_limit = 5 WHERE account_id = $1`, accountID); err != nil {
		t.Fatal(err)
	}
	const vlc, aegis, newpipe = "org.videolan.vlc", "com.beemdevelopment.aegis", "org.schabi.newpipe"
	if _, err := st.db.ExecContext(ctx,
		`UPDATE app_catalog SET enabled = TRUE WHERE package_name IN ($1, $2, $3)`, vlc, aegis, newpipe); err != nil {
		t.Fatal(err)
	}
	packages := func(apps []AppCatalogEntry) map[string]bool {
		set := map[string]bool{}
		for _, app := range apps {
			set[app.PackageName] = true
		}
		return set
	}

	// Apps chosen when a runtime is created belong to that runtime only.
	first, err := st.CreateRuntime(ctx, accountID, CreateRuntimeInput{Name: "Apps one", AppPackages: []string{vlc, aegis}})
	if err != nil {
		t.Fatalf("create runtime with apps: %v", err)
	}
	second, err := st.CreateRuntime(ctx, accountID, CreateRuntimeInput{Name: "Apps two"})
	if err != nil {
		t.Fatalf("create runtime without apps: %v", err)
	}
	if apps, err := st.RuntimeAppSelections(ctx, accountID, first.ID); err != nil || len(apps) != 2 || !packages(apps)[vlc] || !packages(apps)[aegis] {
		t.Fatalf("first runtime apps = %v err=%v, want VLC and Aegis", apps, err)
	}
	if apps, err := st.RuntimeAppSelections(ctx, accountID, second.ID); err != nil || len(apps) != 0 {
		t.Fatalf("second runtime apps = %v err=%v, want none", apps, err)
	}

	// The catalog marks what the given runtime selected.
	for _, check := range []struct {
		runtimeID string
		selected  bool
	}{{first.ID, true}, {second.ID, false}} {
		page, err := st.ListAppCatalogPage(ctx, AppCatalogQuery{AccountID: accountID, RuntimeID: check.runtimeID, Search: "vlc"})
		if err != nil || len(page.Items) == 0 || page.Items[0].PackageName != vlc || page.Items[0].Selected != check.selected {
			t.Fatalf("VLC on runtime %s: page=%+v err=%v, want selected=%v", check.runtimeID, page, err, check.selected)
		}
	}

	if apps, err := st.ReplaceRuntimeAppSelections(ctx, accountID, second.ID, []string{newpipe}); err != nil || len(apps) != 1 || apps[0].PackageName != newpipe {
		t.Fatalf("replace second runtime apps = %v err=%v, want NewPipe", apps, err)
	}
	if _, err := st.RuntimeAppSelections(ctx, uuid.NewString(), first.ID); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("another account read the runtime's apps: err=%v", err)
	}
	if _, err := st.ReplaceRuntimeAppSelections(ctx, uuid.NewString(), first.ID, []string{newpipe}); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("another account changed the runtime's apps: err=%v", err)
	}
	if _, err := st.ReplaceRuntimeAppSelections(ctx, accountID, first.ID, []string{"org.missing.app"}); !errors.Is(err, ErrAppSelection) {
		t.Fatalf("unknown app: err=%v, want %v", err, ErrAppSelection)
	}

	// Pages follow one another without repeating an app.
	one, err := st.ListAppCatalogPage(ctx, AppCatalogQuery{Limit: 2})
	if err != nil || len(one.Items) != 2 || one.NextOffset == nil || *one.NextOffset != 2 {
		t.Fatalf("first page = %+v err=%v, want two apps and a next page at 2", one, err)
	}
	two, err := st.ListAppCatalogPage(ctx, AppCatalogQuery{Limit: 2, Offset: *one.NextOffset})
	if err != nil || len(two.Items) == 0 || packages(one.Items)[two.Items[0].PackageName] {
		t.Fatalf("second page = %+v err=%v, want apps after the first page", two, err)
	}

	// An app release from before runtime selections sets every runtime.
	if _, err := st.ReplaceAccountAppSelections(ctx, accountID, []string{vlc}); err != nil {
		t.Fatalf("account-wide selections: %v", err)
	}
	for _, id := range []string{first.ID, second.ID} {
		if apps, err := st.RuntimeAppSelections(ctx, accountID, id); err != nil || len(apps) != 1 || apps[0].PackageName != vlc {
			t.Fatalf("runtime %s after account-wide save = %v err=%v, want VLC", id, apps, err)
		}
	}
}
