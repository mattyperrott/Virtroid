package httpapi

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// relayLegTestTimer dials nothing: each connect takes the next of delays (or
// fails for a negative one) and hands back one end of a pipe.
func relayLegTestTimer(delays ...time.Duration) (*relayLegTimer, *atomic.Int32) {
	var calls atomic.Int32
	clock := time.Unix(1_800_000_000, 0)
	timer := newRelayLegTimer()
	timer.now = func() time.Time { return clock }
	timer.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		i := int(calls.Add(1)) - 1
		delay := delays[i%len(delays)]
		if delay < 0 {
			return nil, errors.New("unreachable")
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		client, server := net.Pipe()
		_ = server.Close()
		return client, nil
	}
	return timer, &calls
}

func TestRelayLegRoundTripIsTheQuickestConnect(t *testing.T) {
	timer, calls := relayLegTestTimer(60*time.Millisecond, 10*time.Millisecond, 40*time.Millisecond)
	rtt, ok := timer.roundTrip(context.Background(), "[2001:db8::1]:9443")
	if !ok {
		t.Fatal("no round trip")
	}
	if rtt < 10*time.Millisecond || rtt >= 40*time.Millisecond {
		t.Fatalf("round trip %v, want the 10 ms connect", rtt)
	}
	if got := calls.Load(); got != relayLegProbes {
		t.Fatalf("%d connects, want %d", got, relayLegProbes)
	}
}

func TestRelayLegRoundTripIgnoresFailedConnects(t *testing.T) {
	timer, _ := relayLegTestTimer(-1, 20*time.Millisecond, -1)
	rtt, ok := timer.roundTrip(context.Background(), "[2001:db8::1]:9443")
	if !ok || rtt < 20*time.Millisecond {
		t.Fatalf("round trip %v %v, want the one 20 ms connect", rtt, ok)
	}
}

func TestRelayLegRoundTripLeavesOutLostPackets(t *testing.T) {
	// A connect still waiting at the timeout lost its SYN: not a round trip.
	timer, _ := relayLegTestTimer(relayLegTimeout*2, 15*time.Millisecond, relayLegTimeout*2)
	start := time.Now()
	rtt, ok := timer.roundTrip(context.Background(), "[2001:db8::1]:9443")
	if !ok || rtt >= relayLegTimeout {
		t.Fatalf("round trip %v %v, want the 15 ms connect", rtt, ok)
	}
	if waited := time.Since(start); waited > relayLegTimeout+500*time.Millisecond {
		t.Fatalf("waited %v, past the %v timeout", waited, relayLegTimeout)
	}
}

func TestRelayLegRoundTripUnreachable(t *testing.T) {
	timer, _ := relayLegTestTimer(-1)
	if rtt, ok := timer.roundTrip(context.Background(), "[2001:db8::1]:9443"); ok {
		t.Fatalf("round trip %v from a node that refused every connect", rtt)
	}
	var none *relayLegTimer
	if _, ok := none.roundTrip(context.Background(), "[2001:db8::1]:9443"); ok {
		t.Fatal("a nil timer measured something")
	}
}

func TestRelayLegRoundTripIsReusedUntilItAges(t *testing.T) {
	timer, calls := relayLegTestTimer(5 * time.Millisecond)
	clock := time.Unix(1_800_000_000, 0)
	timer.now = func() time.Time { return clock }
	first, _ := timer.roundTrip(context.Background(), "[2001:db8::1]:9443")
	clock = clock.Add(relayLegMaxAge - time.Second)
	if again, ok := timer.roundTrip(context.Background(), "[2001:db8::1]:9443"); !ok || again != first {
		t.Fatalf("reused %v %v, want %v", again, ok, first)
	}
	if got := calls.Load(); got != relayLegProbes {
		t.Fatalf("%d connects, want only the first timing's %d", got, relayLegProbes)
	}
	clock = clock.Add(2 * time.Second)
	if _, ok := timer.roundTrip(context.Background(), "[2001:db8::1]:9443"); !ok {
		t.Fatal("no fresh round trip")
	}
	if got := calls.Load(); got != 2*relayLegProbes {
		t.Fatalf("%d connects, want a fresh timing once the old one aged", got)
	}
	// Another node is timed on its own, and aged entries are dropped.
	clock = clock.Add(relayLegMaxAge)
	if _, ok := timer.roundTrip(context.Background(), "[2001:db8::2]:9443"); !ok {
		t.Fatal("no round trip to the second node")
	}
	if _, kept := timer.last["[2001:db8::1]:9443"]; kept {
		t.Fatal("an aged timing was kept")
	}
}
