package store

import "testing"

func TestCreditRunway(t *testing.T) {
	const vmPerHour = 57_000
	// The numbers from the outage of 2026-11-05: Aleph asked for 2,837,098.5.
	const perDay = 1_469_099
	const needed = perDay + 24*vmPerHour
	if needed != 2_837_099 {
		t.Fatalf("needed = %d", int64(needed))
	}
	cases := []struct {
		name     string
		credits  int64
		perDay   int64
		level    string
		below    bool
		daysLeft float64
	}{
		{"the outage", 1_659_418, perDay, CreditLevelBad, true, 0},
		{"exactly enough", needed, perDay, CreditLevelBad, false, 0},
		{"three days", needed + 3*perDay, perDay, CreditLevelBad, false, 3},
		{"four days", needed + 4*perDay, perDay, CreditLevelWarn, false, 4},
		{"two weeks", needed + 14*perDay, perDay, CreditLevelWarn, false, 14},
		{"fifteen days", needed + 15*perDay, perDay, CreditLevelGood, false, 15},
		{"not spending", 10_000_000, 0, CreditLevelGood, false, -1},
		{"not spending, can't afford a VM", 1_000_000, 0, CreditLevelBad, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runway := AlephCreditStatus{Credits: tc.credits, CreditsPerDay: tc.perDay}.Runway(vmPerHour)
			if runway.Level != tc.level || runway.BelowNeeded != tc.below || runway.DaysLeft != tc.daysLeft {
				t.Fatalf("runway = %+v, want level=%s below=%v days=%v", runway, tc.level, tc.below, tc.daysLeft)
			}
		})
	}
}
