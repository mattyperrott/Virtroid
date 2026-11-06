package main

import (
	"context"
	"log"
	"time"
)

const (
	creditWatchEvery = time.Hour
	// creditSpendDays is how far back the spend per day is averaged.
	creditSpendDays = 7
)

// creditReport is a reading of the credits of the account that pays for VMs.
type creditReport struct {
	Address       string `json:"address"`
	Credits       int64  `json:"credits"`
	CreditsPerDay int64  `json:"credits_per_day"`
}

// creditWatch tells the control plane how the paying account's credits stand,
// so running short is noticed before Aleph starts refusing VMs. It only reads.
type creditWatch struct {
	address string
	balance func(ctx context.Context, address string) (int64, error)
	spend   func(ctx context.Context, address string, days int, now time.Time) (int64, error)
	report  func(ctx context.Context, report creditReport) error
	now     func() time.Time
	logf    func(format string, args ...any)
}

func newCreditWatch(address string, balance func(context.Context, string) (int64, error),
	spend func(context.Context, string, int, time.Time) (int64, error), report func(context.Context, creditReport) error) *creditWatch {
	return &creditWatch{address: address, balance: balance, spend: spend, report: report, now: time.Now, logf: log.Printf}
}

func (w *creditWatch) runOnce(ctx context.Context) {
	credits, err := w.balance(ctx, w.address)
	if err != nil {
		w.logf("provisioner credits: credit balance of %s: %v", w.address, err)
		return
	}
	perDay, err := w.spend(ctx, w.address, creditSpendDays, w.now())
	if err != nil {
		w.logf("provisioner credits: credit spend of %s: %v", w.address, err)
		return
	}
	if err := w.report(ctx, creditReport{Address: w.address, Credits: credits, CreditsPerDay: perDay}); err != nil {
		w.logf("provisioner credits: report to the control plane: %v", err)
	}
}
