package store

import (
	"math"
	"testing"
	"time"
)

func TestSplitByHour(t *testing.T) {
	at := func(clock string) time.Time {
		parsed, err := time.Parse(time.RFC3339, "2026-10-13T"+clock+"Z")
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	parts := splitByHour(at("13:40:00"), at("15:10:30"))
	want := []hourPart{
		{start: at("13:00:00"), duration: 20 * time.Minute},
		{start: at("14:00:00"), duration: time.Hour},
		{start: at("15:00:00"), duration: 10*time.Minute + 30*time.Second},
	}
	if len(parts) != len(want) {
		t.Fatalf("parts = %+v", parts)
	}
	for i := range want {
		if !parts[i].start.Equal(want[i].start) || parts[i].duration != want[i].duration {
			t.Fatalf("part %d = %+v, want %+v", i, parts[i], want[i])
		}
	}
	if parts := splitByHour(at("13:40:00"), at("13:40:00")); len(parts) != 0 {
		t.Fatalf("empty interval split into %+v", parts)
	}
	if parts := splitByHour(at("13:40:00"), at("13:00:00")); len(parts) != 0 {
		t.Fatalf("backwards interval split into %+v", parts)
	}
}

func TestMulDivRound(t *testing.T) {
	for _, tc := range []struct{ a, b, c, want int64 }{
		{60_000_000, 100_000, 3_600_000_000, 1667}, // a minute at 0.10 USDT/hour
		{1 << 30, 50_000, (1 << 30) * 30, 1667},    // a GiB-day at 0.05 USDT/month
		{1, 1, 2, 1},
		{1, 1, 3, 0},
		{0, 100, 7, 0},
		{math.MaxInt64, 1_000_000_000, math.MaxInt64, 1_000_000_000},
	} {
		if got := mulDivRound(tc.a, tc.b, tc.c); got != tc.want {
			t.Fatalf("mulDivRound(%d, %d, %d) = %d, want %d", tc.a, tc.b, tc.c, got, tc.want)
		}
	}
}

func TestPricingMinimumStart(t *testing.T) {
	pricing := Pricing{RuntimeHourMicro: 100_000}
	if !pricing.Enabled() || pricing.MinimumStartMicro() != 16_666 {
		t.Fatalf("pricing = %+v minimum %d", pricing, pricing.MinimumStartMicro())
	}
	if (Pricing{}).Enabled() {
		t.Fatal("zero pricing is enabled")
	}
}
