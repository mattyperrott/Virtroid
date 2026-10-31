package httpapi

import (
	"context"
	"net"
	"sync"
	"time"
)

// A phone whose own path to a runtime's node is slow or loses packets streams
// through the edge instead, whose relay (on this host) forwards to the node.
// The phone times both paths before it connects, but it can only time its own
// half of the relayed one; relayLegTimer times the other half, from this host
// to the node, for the session response.
const (
	relayLegProbes  = 3
	relayLegSpacing = 30 * time.Millisecond
	// A connect still waiting after this lost a packet and is left out.
	relayLegTimeout = 1500 * time.Millisecond
	relayLegMaxAge  = 2 * time.Minute
)

type relayLegTimer struct {
	mu   sync.Mutex
	last map[string]relayLegSample // node address -> its latest timing
	dial func(ctx context.Context, network, address string) (net.Conn, error)
	now  func() time.Time
}

type relayLegSample struct {
	roundTrip time.Duration
	at        time.Time
}

func newRelayLegTimer() *relayLegTimer {
	dialer := &net.Dialer{}
	return &relayLegTimer{
		last: map[string]relayLegSample{},
		dial: dialer.DialContext,
		now:  time.Now,
	}
}

// roundTrip is the round trip from this host to address: the quickest of a
// few TCP connects, each of which takes exactly one round trip. A timing
// younger than relayLegMaxAge is reused. ok is false when no connect completed.
// A round trip under a millisecond is reported as a millisecond.
func (t *relayLegTimer) roundTrip(ctx context.Context, address string) (time.Duration, bool) {
	if t == nil {
		return 0, false
	}
	t.mu.Lock()
	sample, cached := t.last[address]
	t.mu.Unlock()
	if cached && t.now().Sub(sample.at) < relayLegMaxAge {
		return sample.roundTrip, true
	}

	ctx, cancel := context.WithTimeout(ctx, relayLegTimeout)
	defer cancel()
	timings := make(chan time.Duration, relayLegProbes)
	for i := range relayLegProbes {
		go func(wait time.Duration) {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				timings <- 0
				return
			}
			start := time.Now()
			conn, err := t.dial(ctx, "tcp", address)
			if err != nil {
				timings <- 0
				return
			}
			// At least a nanosecond: zero stands for a failed connect.
			elapsed := time.Since(start) + time.Nanosecond
			_ = conn.Close()
			timings <- elapsed
		}(time.Duration(i) * relayLegSpacing)
	}
	var quickest time.Duration
	for range relayLegProbes {
		if elapsed := <-timings; elapsed > 0 && (quickest == 0 || elapsed < quickest) {
			quickest = elapsed
		}
	}
	if quickest == 0 {
		return 0, false
	}
	quickest = quickest.Round(time.Millisecond)
	if quickest < time.Millisecond {
		quickest = time.Millisecond
	}

	now := t.now()
	t.mu.Lock()
	for other, old := range t.last {
		if now.Sub(old.at) >= relayLegMaxAge {
			delete(t.last, other)
		}
	}
	t.last[address] = relayLegSample{roundTrip: quickest, at: now}
	t.mu.Unlock()
	return quickest, true
}
