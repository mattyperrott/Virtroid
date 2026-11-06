package httpapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"virtroid/backend/internal/config"
	"virtroid/backend/internal/store"
)

func TestVMRefusalEndsTheAppsWaitPostgresIntegration(t *testing.T) {
	databaseURL := os.Getenv("VIRTROID_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VIRTROID_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := store.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	live, err := st.ProvisionerWork(ctx)
	if err != nil {
		t.Fatal(err)
	}
	region := "ci-refuse-" + uuid.NewString()[:8]
	provisioner := newProvisionerTestKey(t)
	cfg := config.ServerConfig{
		AppEnv: "production", ProvisionerPublicKey: provisioner.public, ProvisionedRegions: []string{region},
		ProvisionerMaxVMs: len(live) + 2, ProvisionerTrialMaxVMs: len(live) + 2,
		ProvisionerSetupTimeout: 30 * time.Minute, ProvisionerRetireAfter: time.Hour,
	}
	api := &API{cfg: cfg, store: st}
	handler := New(cfg, st)

	accountID := uuid.NewString()
	if _, err := st.BootstrapAccountWithIdentity(ctx, accountID, uuid.NewString(), "CI device", newProvisionerTestKey(t).public, store.CreateRuntimeInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer func() { _ = st.DeleteAccount(context.Background(), accountID) }()
	if err := st.SetAccountRuntimeRegion(ctx, accountID, region); err != nil {
		t.Fatal(err)
	}

	// What the app gets, for a start and for a deletion.
	ask := func(cleanupRuntimeID string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		resp := httptest.NewRecorder()
		api.writeNoReadyHost(resp, httptest.NewRequest(http.MethodPost, "/", nil), accountID, cleanupRuntimeID, store.ErrNoReadyHost)
		var body map[string]any
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("response %q: %v", resp.Body.String(), err)
		}
		return resp, body
	}
	report := func(vmID string, body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, signedTestRequest(t, provisioner, provisionerRequestID, http.MethodPost,
			"/api/v1/internal/provisioner/vms/"+vmID+"/report", raw))
		return resp
	}

	_, body := ask("")
	if body["code"] != store.RuntimeHostProvisioningCode {
		t.Fatalf("before any refusal: %v", body)
	}
	vm, err := st.EnsureAccountVM(ctx, accountID, region, api.provisioningPolicy(), store.VMRequest{})
	if err != nil {
		t.Fatal(err)
	}

	const refusal = "Aleph refused the VM (HTTP 422), most likely for lack of credits: the paying account holds 1,659,418"
	if resp := report(vm.ID, map[string]any{"refusal": refusal}); resp.Code != http.StatusOK {
		t.Fatalf("refusal report = %d %s", resp.Code, resp.Body.String())
	}
	resp, body := ask("")
	if resp.Code != http.StatusServiceUnavailable || resp.Header().Get("Retry-After") == "" || body["code"] != store.RuntimeHostUnavailableCode {
		t.Fatalf("while refused = %d %v", resp.Code, body)
	}
	// The app shows this text as it is. It must not name the cause to everyone
	// waiting, nor contain the words the app replaces with a timeout message.
	message := fmt.Sprint(body["error"])
	for _, word := range []string{"credit", "Aleph", "timed out", "timeout", "viewer"} {
		if strings.Contains(message, word) {
			t.Fatalf("message %q contains %q", message, word)
		}
	}
	if _, body = ask(uuid.NewString()); !strings.Contains(fmt.Sprint(body["error"]), "Nothing was deleted") {
		t.Fatalf("for a deletion: %v", body)
	}
	if same, err := st.ProvisionedVM(ctx, vm.ID); err != nil || same.Desired != "present" || same.Refusal != refusal || same.Failure != "" {
		t.Fatalf("the refused VM should stay, to be asked for again: %+v, %v", same, err)
	}

	// Credits arrive and the create goes through: the app waits again.
	itemHash := strings.Repeat("c", 64)
	if resp := report(vm.ID, map[string]any{"state": "creating", "item_hash": itemHash, "crn_hash": strings.Repeat("d", 64)}); resp.Code != http.StatusOK {
		t.Fatalf("creating report = %d %s", resp.Code, resp.Body.String())
	}
	if _, body = ask(""); body["code"] != store.RuntimeHostProvisioningCode {
		t.Fatalf("after the refusal lifted: %v", body)
	}

	// A VM only goes back to requested with the reason.
	if resp := report(vm.ID, map[string]any{"state": "requested"}); resp.Code != http.StatusConflict {
		t.Fatalf("back to requested without a refusal = %d %s", resp.Code, resp.Body.String())
	}
	// Aleph rejects the message it had accepted: nothing exists, so the hash goes.
	if resp := report(vm.ID, map[string]any{"state": "requested", "refusal": refusal}); resp.Code != http.StatusOK {
		t.Fatalf("rejected report = %d %s", resp.Code, resp.Body.String())
	}
	back, err := st.ProvisionedVM(ctx, vm.ID)
	if err != nil || back.State != store.ProvisionedVMRequested || back.ItemHash != "" || back.CRNHash != "" || back.Refusal != refusal {
		t.Fatalf("after the rejection: %+v, %v", back, err)
	}
	if _, body = ask(""); body["code"] != store.RuntimeHostUnavailableCode {
		t.Fatalf("after the rejection: %v", body)
	}
	// A refusal can't be pinned on a VM that is past needing a create.
	if resp := report(vm.ID, map[string]any{"state": "creating", "item_hash": itemHash}); resp.Code != http.StatusOK {
		t.Fatalf("creating report = %d %s", resp.Code, resp.Body.String())
	}
	if resp := report(vm.ID, map[string]any{"refusal": "late"}); resp.Code != http.StatusOK {
		t.Fatalf("late refusal = %d %s", resp.Code, resp.Body.String())
	}
	if late, err := st.ProvisionedVM(ctx, vm.ID); err != nil || late.Refusal != "" {
		t.Fatalf("a refusal stuck to a creating VM: %+v, %v", late, err)
	}
	if resp := report(vm.ID, map[string]any{"state": "requested", "refusal": refusal}); resp.Code != http.StatusOK {
		t.Fatalf("rejected report = %d %s", resp.Code, resp.Body.String())
	}

	// Nobody tops up: at the setup timeout the VM retires, saying why.
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `UPDATE provisioned_vms SET requested_at = NOW() - INTERVAL '31 minutes' WHERE id = $1`, vm.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReconcileProvisionedVMs(ctx, api.provisioningPolicy()); err != nil {
		t.Fatal(err)
	}
	retired, err := st.ProvisionedVM(ctx, vm.ID)
	if err != nil || retired.Desired != "absent" || retired.RetireReason != "setup-timeout" || retired.Failure != refusal {
		t.Fatalf("after the timeout: %+v, %v", retired, err)
	}
	if resp := report(vm.ID, map[string]any{"state": "deleted"}); resp.Code != http.StatusOK {
		t.Fatalf("deleted report = %d %s", resp.Code, resp.Body.String())
	}
}

func testEncryptionKey(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(der)
}

func TestOperatorNoticesReachOnlyOperatorsPostgresIntegration(t *testing.T) {
	databaseURL := os.Getenv("VIRTROID_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VIRTROID_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := store.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	provisioner := newProvisionerTestKey(t)
	cfg := config.ServerConfig{
		AppEnv: "production", ProvisionerPublicKey: provisioner.public, ProvisionedRegions: []string{"ci-notice"},
		ProvisionerMaxVMs: 1, RuntimeVMCostHourMicro: 57_000,
	}
	handler := New(cfg, st)

	newDevice := func() (string, string) {
		t.Helper()
		accountID, deviceID := uuid.NewString(), uuid.NewString()
		if _, err := st.BootstrapAccountWithIdentity(ctx, accountID, deviceID, "CI device", newProvisionerTestKey(t).public, store.CreateRuntimeInput{}); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
		t.Cleanup(func() { _ = st.DeleteAccount(context.Background(), accountID) })
		if err := st.UpsertDeviceNotificationSubscription(ctx, store.DeviceNotificationSubscription{
			AccountID: accountID, DeviceID: deviceID, EncryptionPublicKey: testEncryptionKey(t),
		}); err != nil {
			t.Fatal(err)
		}
		return accountID, deviceID
	}
	operatorAccount, operatorDevice := newDevice()
	userAccount, userDevice := newDevice()
	if _, err := db.ExecContext(ctx, `UPDATE account_entitlements SET source = 'operator' WHERE account_id = $1`, operatorAccount); err != nil {
		t.Fatal(err)
	}
	address := "0x" + strings.Repeat("a", 40)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM aleph_credit_status WHERE address = $1`, address) })

	post := func(body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, signedTestRequest(t, provisioner, provisionerRequestID, http.MethodPost, "/api/v1/internal/provisioner/credits", raw))
		return resp
	}
	pending := func(accountID, deviceID string) []store.RuntimeNotificationDelivery {
		t.Helper()
		deliveries, err := st.ListPendingNotificationDeliveries(ctx, accountID, deviceID, 20, nil)
		if err != nil {
			t.Fatal(err)
		}
		return deliveries
	}

	// Plenty: recorded, and nobody is bothered.
	if resp := post(map[string]any{"address": address, "credits": 118_746_705, "credits_per_day": 1_469_099}); resp.Code != http.StatusOK {
		t.Fatalf("credits = %d %s", resp.Code, resp.Body.String())
	}
	if got := pending(operatorAccount, operatorDevice); len(got) != 0 {
		t.Fatalf("notified with plenty of credits: %+v", got)
	}
	latest, err := st.LatestAlephCreditStatus(ctx)
	if err != nil || latest == nil || latest.Address != address || latest.Credits != 118_746_705 {
		t.Fatalf("latest = %+v, %v", latest, err)
	}

	// Short: the operator is told, once however often it's read, and nobody else.
	for range 3 {
		if resp := post(map[string]any{"address": address, "credits": 1_659_418, "credits_per_day": 1_469_099}); resp.Code != http.StatusOK {
			t.Fatalf("credits = %d %s", resp.Code, resp.Body.String())
		}
	}
	notices := pending(operatorAccount, operatorDevice)
	if len(notices) != 1 {
		t.Fatalf("operator notices = %d, want 1", len(notices))
	}
	if got := pending(userAccount, userDevice); len(got) != 0 {
		t.Fatalf("an ordinary user was told: %+v", got)
	}

	// The phone's ack clears it.
	if err := st.MarkNotificationDeliveryDelivered(ctx, operatorAccount, operatorDevice, notices[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := pending(operatorAccount, operatorDevice); len(got) != 0 {
		t.Fatalf("still pending after the ack: %+v", got)
	}
	// Not by another account's device.
	if err := st.MarkNotificationDeliveryDelivered(ctx, userAccount, userDevice, notices[0].ID); err == nil {
		t.Fatal("another account acked the operator's notice")
	}

	for name, body := range map[string]map[string]any{
		"not an address": {"address": "owner", "credits": 1, "credits_per_day": 1},
		"negative spend": {"address": address, "credits": 1, "credits_per_day": -1},
	} {
		if resp := post(body); resp.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", name, resp.Code, resp.Body.String())
		}
	}
	// Only the provisioner can report this.
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, signedTestRequest(t, newProvisionerTestKey(t), provisionerRequestID, http.MethodPost, "/api/v1/internal/provisioner/credits",
		[]byte(`{"address":"`+address+`","credits":1,"credits_per_day":0}`)))
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("another key = %d %s", resp.Code, resp.Body.String())
	}
}

// The app's NotificationRelayService refuses a notice with any other keys, and
// only puts up an Android notification for this shape.
func TestOperatorNoticeHasTheShapeTheAppShows(t *testing.T) {
	now := time.Now().UTC()
	raw, err := encodeOperatorNoticePayload(uuid.NewString(), strings.Repeat("🙂", maxNotificationTitleRunes+5), now)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	want := []string{"version", "event_id", "package_name", "app_label", "posted_at", "title"}
	if len(payload) != len(want) {
		t.Fatalf("payload = %v", payload)
	}
	for _, key := range want {
		if _, ok := payload[key]; !ok {
			t.Fatalf("payload lacks %s: %v", key, payload)
		}
	}
	title := fmt.Sprint(payload["title"])
	if !utf8.ValidString(title) || utf8.RuneCountInString(title) != maxNotificationTitleRunes {
		t.Fatalf("title has %d runes, want %d", utf8.RuneCountInString(title), maxNotificationTitleRunes)
	}
	if !validRuntimeNotification(runtimeNotificationEvent{
		EventID: fmt.Sprint(payload["event_id"]), PackageName: operatorNoticePackage, AppLabel: operatorNoticeLabel,
		Title: title, PostedAt: now,
	}) {
		t.Fatal("the server's own check of a runtime notification refuses this shape")
	}
}

func TestCreditNoticeTitles(t *testing.T) {
	const vmPerHour = 57_000
	address := "0x0aBe3c402B41CC339B7A101Ad940214cFB947a08"
	for _, tc := range []struct {
		credits, perDay int64
		want            string
	}{
		{1_659_418, 1_469_099, "Aleph credits are too low for new VMs: 1,659,418 left, about 2,837,099 needed. Top up 0x0aBe…7a08."},
		{2_837_099 + 12*1_469_099, 1_469_099, "Aleph will refuse new VMs in about 12 days: 20,466,287 credits left, 1,469,099 a day. Top up 0x0aBe…7a08."},
		{2_837_099 + 1_000_000, 1_469_099, "Aleph will refuse new VMs within a day or so: 3,837,099 credits left, 1,469,099 a day. Top up 0x0aBe…7a08."},
	} {
		status := store.AlephCreditStatus{Address: address, Credits: tc.credits, CreditsPerDay: tc.perDay}
		if got := creditNoticeTitle(status, status.Runway(vmPerHour)); got != tc.want {
			t.Fatalf("title = %q, want %q", got, tc.want)
		}
	}
}
