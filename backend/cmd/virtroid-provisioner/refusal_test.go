package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"virtroid/backend/internal/alephapi"
)

type fakeMessages struct {
	message alephapi.Message
	err     error
	calls   int
}

func (f *fakeMessages) Message(context.Context, string) (alephapi.Message, error) {
	f.calls++
	return f.message, f.err
}

// What Aleph really answered for the instance it refused on 2026-11-05.
func rejectedForCredits(t *testing.T) alephapi.Message {
	t.Helper()
	var message alephapi.Message
	if err := json.Unmarshal([]byte(`{"status":"rejected","error_code":6,"details":{"errors":[{"account_credits":"1659418","min_runtime_days":1,"required_credits":"2837098.518514818475161600"}]}}`), &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func refusedCreate() error {
	return fmt.Errorf("%w: aleph instance create: exit status 1: Error: API request failed (HTTP 422)", errCreateRefused)
}

func TestRefusedCreateIsReportedAndAskedForAgain(t *testing.T) {
	cp := &fakeControlPlane{vms: []provisionedVM{{ID: "pvm-1", Region: "snp-eu", State: vmStateRequested, Desired: "present"}}}
	aleph := &fakeAleph{createErr: refusedCreate()}
	r := testReconciler(t, cp, aleph)
	now := time.Date(2026, 11, 5, 16, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	r.payerCredits = func(context.Context) (int64, error) { return 1_659_418, nil }
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := cp.last(t)
	if got.State != "" || got.Failure != "" || !strings.Contains(got.Refusal, "holds 1,659,418") {
		t.Fatalf("report = %+v", got)
	}
	// It left nothing behind, so nothing has to settle before asking again.
	if record, err := r.loadCreate("pvm-1"); err != nil || !record.SubmittedAt.IsZero() {
		t.Fatalf("create record = %+v, %v", record, err)
	}

	now = now.Add(refusalRetryEvery - time.Second)
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(aleph.created) != 1 {
		t.Fatalf("asked again after %v: %d creates", refusalRetryEvery-time.Second, len(aleph.created))
	}

	// Credits arrive: the same VM goes ahead, and the refusal is over.
	aleph.createErr = nil
	now = now.Add(time.Second)
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := cp.last(t); len(aleph.created) != 2 || got.State != vmStateCreating || got.Refusal != "" {
		t.Fatalf("created=%d report=%+v", len(aleph.created), got)
	}
	if _, refused := r.lastRefused["pvm-1"]; refused {
		t.Fatal("still holding off after a create went through")
	}
}

func TestRefusalWithoutABalanceStillSaysAlephRefused(t *testing.T) {
	cp := &fakeControlPlane{vms: []provisionedVM{{ID: "pvm-1", Region: "snp-eu", State: vmStateRequested, Desired: "present"}}}
	r := testReconciler(t, cp, &fakeAleph{createErr: refusedCreate()})
	r.payerCredits = func(context.Context) (int64, error) { return 0, errors.New("Aleph API down") }
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := cp.last(t); got.Refusal != "Aleph refused the VM (HTTP 422)" {
		t.Fatalf("report = %+v", got)
	}
}

// Aleph can accept a create and reject it a minute later, which is how a VM
// logged as created on 2026-11-05 turned out never to exist.
func TestRejectedCreateGoesBackToBeingRequested(t *testing.T) {
	cp := &fakeControlPlane{vms: []provisionedVM{{
		ID: "pvm-1", Region: "snp-eu", State: vmStateCreating, Desired: "present", ItemHash: hash, CRNHash: crnA,
	}}}
	aleph := &fakeAleph{}
	r := testReconciler(t, cp, aleph)
	now := time.Date(2026, 11, 5, 16, 30, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	r.messages = &fakeMessages{message: rejectedForCredits(t)}
	if err := r.saveCreate("pvm-1", createRecord{CRNHash: crnA, SubmittedAt: now, ItemHash: hash}); err != nil {
		t.Fatal(err)
	}
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "Aleph refused the VM for lack of credits: the paying account holds 1,659,418 and needs 2,837,099"
	if got := cp.last(t); got.State != vmStateRequested || got.Refusal != want || got.Failure != "" {
		t.Fatalf("report = %+v", got)
	}
	if record, err := r.loadCreate("pvm-1"); err != nil || record.ItemHash != "" {
		t.Fatalf("the rejected create is still recorded: %+v, %v", record, err)
	}

	// The control plane now has it requested: not adopted, and asked for again.
	cp.vms[0].State, cp.vms[0].ItemHash, cp.vms[0].CRNHash = vmStateRequested, "", ""
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(aleph.created) != 0 {
		t.Fatalf("asked again at once: %d creates", len(aleph.created))
	}
	now = now.Add(refusalRetryEvery)
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := cp.last(t); len(aleph.created) != 1 || got.State != vmStateCreating {
		t.Fatalf("created=%d report=%+v", len(aleph.created), got)
	}
}

func TestCreateAlephHasNotRejectedIsLeftAlone(t *testing.T) {
	for name, messages := range map[string]*fakeMessages{
		"not seen yet": {err: alephapi.ErrNotFound},
		"unreachable":  {err: errors.New("Aleph API down")},
		"pending":      {message: alephapi.Message{Status: "pending"}},
		"processed":    {message: alephapi.Message{Status: "processed"}},
	} {
		t.Run(name, func(t *testing.T) {
			cp := &fakeControlPlane{vms: []provisionedVM{{
				ID: "pvm-1", Region: "snp-eu", State: vmStateCreating, Desired: "present", ItemHash: hash, CRNHash: crnA,
			}}}
			r := testReconciler(t, cp, &fakeAleph{})
			now := time.Date(2026, 11, 5, 16, 30, 0, 0, time.UTC)
			r.now = func() time.Time { return now }
			r.messages = messages
			for range 3 {
				if err := r.runOnce(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if len(cp.reports) != 0 {
				t.Fatalf("reports = %+v", cp.reports)
			}
			if messages.calls != 1 {
				t.Fatalf("asked Aleph %d times in one interval", messages.calls)
			}
			now = now.Add(rejectionCheckEvery)
			if err := r.runOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if messages.calls != 2 {
				t.Fatalf("didn't ask again after the interval: %d calls", messages.calls)
			}
		})
	}
}

func TestListedInstanceIsNotCheckedForRejection(t *testing.T) {
	cp := &fakeControlPlane{vms: []provisionedVM{{
		ID: "pvm-1", Region: "snp-eu", State: vmStateCreating, Desired: "present", ItemHash: hash, CRNHash: crnA,
	}}}
	messages := &fakeMessages{message: rejectedForCredits(t)}
	r := testReconciler(t, cp, &fakeAleph{instances: []alephInstance{{ItemHash: hash, Name: "virtroid-pvm-1", NodeHash: crnA}}})
	r.messages = messages
	if err := r.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if messages.calls != 0 || len(cp.reports) != 0 {
		t.Fatalf("calls=%d reports=%+v", messages.calls, cp.reports)
	}
}

func TestIsCreateRefusal(t *testing.T) {
	if !isCreateRefusal("aleph instance create: exit status 1: Error: API request failed (HTTP 422)") {
		t.Fatal("the CLI's own message isn't recognized")
	}
	for _, other := range []string{"API request failed (HTTP 503)", "allocation timed out", ""} {
		if isCreateRefusal(other) {
			t.Fatalf("%q counted as a refusal", other)
		}
	}
}

func TestCLIDriverRecognizesARefusalAndTransfersCredits(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := filepath.Join(dir, "aleph")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
echo "$@" >> `+argsFile+`
case "$*" in
  *"instance create refused"*) echo "Error: API request failed (HTTP 422)" >&2; exit 1 ;;
  *"instance create down"*) echo "Error: API request failed (HTTP 503)" >&2; exit 1 ;;
  *"credit transfer"*) echo '{"sender":"0x4631","type":"POST","item_hash":"`+hash+`","channel":"ALEPH_CREDIT"}' ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	driver := cliDriver{bin: script, account: "provisioner", timeout: 10 * time.Second}
	ctx := context.Background()

	if _, err := driver.Create(ctx, createSpec{Name: "refused"}); !errors.Is(err, errCreateRefused) {
		t.Fatalf("HTTP 422: %v", err)
	}
	if _, err := driver.Create(ctx, createSpec{Name: "down"}); err == nil || errors.Is(err, errCreateRefused) {
		t.Fatalf("HTTP 503 counted as a refusal: %v", err)
	}
	const owner = "0x0aBe3c402B41CC339B7A101Ad940214cFB947a08"
	message, err := driver.TransferCredits(ctx, owner, 5_000_000)
	if err != nil || message != hash {
		t.Fatalf("transfer = %q, %v", message, err)
	}
	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	want := "--json credit transfer --to " + owner + " --amount 5000000 --yes --account provisioner"
	if got := lines[len(lines)-1]; got != want {
		t.Fatalf("transfer args = %q, want %q", got, want)
	}
}

func TestCreditWatchReportsThePayer(t *testing.T) {
	const payer = "0x0aBe3c402B41CC339B7A101Ad940214cFB947a08"
	var reports []creditReport
	var logs []string
	watch := newCreditWatch(payer,
		func(_ context.Context, address string) (int64, error) {
			if address != payer {
				t.Fatalf("read the balance of %s", address)
			}
			return 118_746_705, nil
		},
		func(_ context.Context, address string, days int, _ time.Time) (int64, error) {
			if address != payer || days != creditSpendDays {
				t.Fatalf("read the spend of %s over %d days", address, days)
			}
			return 1_469_099, nil
		},
		func(_ context.Context, report creditReport) error {
			reports = append(reports, report)
			return nil
		})
	watch.logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	watch.runOnce(context.Background())
	if len(reports) != 1 || reports[0] != (creditReport{Address: payer, Credits: 118_746_705, CreditsPerDay: 1_469_099}) {
		t.Fatalf("reports = %+v", reports)
	}

	// A reading it can't complete isn't reported: the console then shows the
	// last one aging, rather than zero spend looking like plenty of runway.
	watch.spend = func(context.Context, string, int, time.Time) (int64, error) { return 0, errors.New("Aleph API down") }
	watch.runOnce(context.Background())
	if len(reports) != 1 || len(logs) != 1 {
		t.Fatalf("reports=%+v logs=%q", reports, logs)
	}
}
