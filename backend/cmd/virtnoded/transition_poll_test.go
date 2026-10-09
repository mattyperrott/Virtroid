package main

import "testing"

func TestRuntimeIsTransitioning(t *testing.T) {
	cases := []struct {
		name       string
		runtime    runtimeAssignment
		transition bool
	}{
		{"start requested", runtimeAssignment{DesiredState: "running", Status: "starting", ConnectionStatus: "connecting"}, true},
		{"container up, Android still booting", runtimeAssignment{DesiredState: "running", Status: "running", ConnectionStatus: "connecting"}, true},
		{"running and online", runtimeAssignment{DesiredState: "running", Status: "running", ConnectionStatus: "online"}, false},
		{"stop requested while running", runtimeAssignment{DesiredState: "stopped", Status: "running", ConnectionStatus: "disconnecting"}, true},
		{"stopping", runtimeAssignment{DesiredState: "stopped", Status: "stopping"}, true},
		{"stopped", runtimeAssignment{DesiredState: "stopped", Status: "stopped", ConnectionStatus: "offline"}, false},
		{"errored runtime is left to the full pass", runtimeAssignment{DesiredState: "running", Status: "error", ConnectionStatus: "offline"}, false},
		{"wipe is left to the full pass", runtimeAssignment{DesiredState: "running", Status: "starting", WipeRequested: true}, false},
		{"cleanup is left to the full pass", runtimeAssignment{DesiredState: "stopped", Status: "running", CleanupPending: true}, false},
		{"deletion is left to the full pass", runtimeAssignment{DesiredState: "deleted", Status: "running"}, false},
	}
	for _, tc := range cases {
		if got := runtimeIsTransitioning(tc.runtime); got != tc.transition {
			t.Errorf("%s: runtimeIsTransitioning = %v, want %v", tc.name, got, tc.transition)
		}
	}
}

func TestTransitioningAssignmentsKeepsOnlyTransitions(t *testing.T) {
	assignments := []runtimeAssignment{
		{ID: "online", DesiredState: "running", Status: "running", ConnectionStatus: "online"},
		{ID: "booting", DesiredState: "running", Status: "starting", ConnectionStatus: "connecting"},
		{ID: "stopped", DesiredState: "stopped", Status: "stopped"},
	}
	got := transitioningAssignments(assignments)
	if len(got) != 1 || got[0].ID != "booting" {
		t.Fatalf("transitioningAssignments = %+v, want only the booting runtime", got)
	}
}
