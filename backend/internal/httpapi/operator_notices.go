package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"virtroid/backend/internal/alephapi"
	"virtroid/backend/internal/push"
	"virtroid/backend/internal/store"
)

const (
	// The app shows a notice as a notification from this name and package.
	operatorNoticeLabel   = "Virtroid operator"
	operatorNoticePackage = "io.virtroid.client"

	// How often a condition that persists is repeated, per device. Being days
	// from trouble can wait; being in it can't.
	creditWarnNoticeEvery     = 3 * 24 * time.Hour
	creditCriticalNoticeEvery = 24 * time.Hour
	vmRefusedNoticeEvery      = 24 * time.Hour
)

var evmAddressPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// noticeCredits tells the operator when the paying account is running short.
func (a *API) noticeCredits(ctx context.Context, status store.AlephCreditStatus, now time.Time) {
	runway := status.Runway(a.cfg.RuntimeVMCostHourMicro)
	if runway.Level == store.CreditLevelGood {
		return
	}
	every := creditWarnNoticeEvery
	if runway.Level == store.CreditLevelBad {
		every = creditCriticalNoticeEvery
	}
	key := fmt.Sprintf("credits\x00%s\x00%d", runway.Level, now.Unix()/int64(every/time.Second))
	a.enqueueOperatorNotice(ctx, key, creditNoticeTitle(status, runway), now)
}

func creditNoticeTitle(status store.AlephCreditStatus, runway store.CreditRunway) string {
	address := shortEVMAddress(status.Address)
	if runway.BelowNeeded {
		return fmt.Sprintf("Aleph credits are too low for new VMs: %s left, about %s needed. Top up %s.",
			alephapi.GroupDigits(status.Credits), alephapi.GroupDigits(runway.Needed), address)
	}
	days := int(math.Round(runway.DaysLeft))
	when := fmt.Sprintf("in about %d days", days)
	if days <= 1 {
		when = "within a day or so"
	}
	return fmt.Sprintf("Aleph will refuse new VMs %s: %s credits left, %s a day. Top up %s.",
		when, alephapi.GroupDigits(status.Credits), alephapi.GroupDigits(status.CreditsPerDay), address)
}

func shortEVMAddress(address string) string {
	if len(address) < 10 {
		return address
	}
	return address[:6] + "…" + address[len(address)-4:]
}

// noticeVMRefused tells the operator Aleph refused a VM someone is waiting for.
func (a *API) noticeVMRefused(ctx context.Context, vm store.ProvisionedVM) {
	now := time.Now().UTC()
	key := fmt.Sprintf("vm-refused\x00%d", now.Unix()/int64(vmRefusedNoticeEvery/time.Second))
	a.enqueueOperatorNotice(ctx, key, vm.Refusal, now)
}

// enqueueOperatorNotice queues a notice for the operator's devices, end to end
// encrypted like every other notification. A failure is only logged: this runs
// beside the work that prompted it and must not fail it. The same key is one
// event, which the store and the app each take only once.
func (a *API) enqueueOperatorNotice(ctx context.Context, key, title string, now time.Time) {
	eventID := uuid.NewHash(sha256.New(), uuid.NameSpaceOID, []byte("operator-notice\x00"+key), 5).String()
	payload, err := encodeOperatorNoticePayload(eventID, title, now)
	if err != nil {
		log.Printf("operator notice failed: %v", err)
		return
	}
	subscriptions, err := a.store.ListOperatorNotificationSubscriptions(ctx)
	if err != nil {
		log.Printf("operator notice failed: list devices: %v", err)
		return
	}
	if len(subscriptions) == 0 {
		log.Printf("operator notice not sent, no operator device takes notifications: %s", title)
		return
	}
	expiresAt := now.UTC().Add(notificationDeliveryTTL)
	for _, subscription := range subscriptions {
		envelope, err := push.EncryptEnvelope(subscription.EncryptionPublicKey, payload)
		if err != nil {
			log.Printf("operator notice failed: encrypt: %v", err)
			continue
		}
		if _, err := a.store.EnqueueOperatorNoticeDelivery(ctx, store.OperatorNoticeDelivery{
			ID:                 uuid.NewString(),
			EventID:            eventID,
			DeviceID:           subscription.DeviceID,
			EnvelopeCiphertext: envelope,
			ExpiresAt:          expiresAt,
		}); err != nil {
			log.Printf("operator notice failed: queue: %v", err)
			continue
		}
		a.notificationHub.notify(subscription.DeviceID)
	}
}

// encodeOperatorNoticePayload writes a notice in the shape of a runtime's
// notification, the only kind the app puts up as an Android notification (it
// only logs security notices). So it takes no app update to receive one.
func encodeOperatorNoticePayload(eventID, title string, now time.Time) ([]byte, error) {
	return json.Marshal(map[string]any{
		"version":      1,
		"event_id":     eventID,
		"package_name": operatorNoticePackage,
		"app_label":    operatorNoticeLabel,
		"posted_at":    now.UTC().Format(time.RFC3339Nano),
		"title":        boundedNoticeTitle(title),
	})
}

// boundedNoticeTitle keeps a title to what the app accepts: it rejects a longer one outright.
func boundedNoticeTitle(title string) string {
	if utf8.RuneCountInString(title) <= maxNotificationTitleRunes {
		return title
	}
	return string([]rune(title)[:maxNotificationTitleRunes-1]) + "…"
}
