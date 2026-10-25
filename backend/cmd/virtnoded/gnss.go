package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A runtime's location feed. The node serves the runtime's place (from its
// sealed region) on a socket the custom Android image's GNSS HAL reads, so
// apps see an ordinary GPS fix rather than a mock location. It speaks the
// fixed-location protocol of AOSP's default GNSS HAL (hardware/interfaces
// gnss/common/utils/default: DeviceFileReader, FixLocationParser);
// deploy/aosp/gnss-feed.md is the contract. Until a runtime runs that image,
// nothing reads it: stock ReDroid has no HAL that does.
const (
	// gnssFeedDirName is the feed's directory beside the runtime's data. The
	// node owns it and the container sees it read-only, so the guest can
	// neither replace the socket nor point the node anywhere else.
	gnssFeedDirName   = "gnss"
	gnssSocketName    = "gnss.sock"
	gnssFeedMountPath = "/opt/virtroid/gnss"
	// The HAL runs as Android's gps user (AID_GPS); apps run as their own
	// users and cannot connect, so they still need location permission.
	gnssSocketUID  = 1021
	gnssSocketGID  = 1021
	gnssSocketMode = 0o660

	gnssGetLocation = "CMD_GET_LOCATION"
	gnssRecordEnd   = "\n\n\n\n"
	// The HAL waits 20 ms for an answer each second; a client that sends
	// nothing is dropped well before it could hold a handler for long.
	gnssClientTimeout   = 2 * time.Second
	gnssMaxCommandBytes = 64
	gnssMaxClients      = 4
)

// gnssPlace is where a runtime is, from its sealed region.
type gnssPlace struct {
	latitude  float64
	longitude float64
}

// gnssFeeds holds every running runtime's feed, each on its own socket.
type gnssFeeds struct {
	mu    sync.Mutex
	feeds map[string]*gnssFeed
	// uid and gid own each socket; -1 leaves the owner as is (tests).
	uid, gid int
	now      func() time.Time
	noise    func() gnssNoise
}

type gnssFeed struct {
	dir      string
	listener *net.UnixListener
	place    atomic.Pointer[gnssPlace]
	clients  chan struct{}
	done     chan struct{}
}

func newGNSSFeeds(uid, gid int) *gnssFeeds {
	return &gnssFeeds{feeds: map[string]*gnssFeed{}, uid: uid, gid: gid, now: time.Now, noise: realisticGNSSNoise}
}

func gnssFeedDir(runtimeRoot, runtimeID string) string {
	return filepath.Join(runtimeRoot, runtimeID, gnssFeedDirName)
}

// start opens the runtime's feed if it isn't open, with no place yet: until
// one is set the feed answers with no fix.
func (g *gnssFeeds) start(runtimeRoot, runtimeID string) error {
	if g == nil {
		return nil
	}
	if runtimeID == "" || strings.ContainsAny(runtimeID, `/\`) || runtimeID == "." || runtimeID == ".." {
		return errors.New("invalid runtime id")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, open := g.feeds[runtimeID]; open {
		return nil
	}
	dir := gnssFeedDir(runtimeRoot, runtimeID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create location feed directory: %w", err)
	}
	// A socket left by an earlier node process can't be listened on again.
	socketPath := filepath.Join(dir, gnssSocketName)
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove old location socket: %w", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen for the location feed: %w", err)
	}
	listener.SetUnlinkOnClose(true)
	if err := os.Chmod(socketPath, gnssSocketMode); err != nil {
		_ = listener.Close()
		return fmt.Errorf("restrict the location socket: %w", err)
	}
	if g.uid >= 0 {
		if err := os.Chown(socketPath, g.uid, g.gid); err != nil {
			_ = listener.Close()
			return fmt.Errorf("hand the location socket to the GNSS HAL: %w", err)
		}
	}
	feed := &gnssFeed{dir: dir, listener: listener, clients: make(chan struct{}, gnssMaxClients), done: make(chan struct{})}
	g.feeds[runtimeID] = feed
	go g.serve(feed)
	return nil
}

// set gives the runtime's feed its place; nil means no fix. A runtime whose
// feed isn't open is ignored: the next start sets it again.
func (g *gnssFeeds) set(runtimeID string, place *gnssPlace) {
	if g == nil {
		return
	}
	g.mu.Lock()
	feed := g.feeds[runtimeID]
	g.mu.Unlock()
	if feed != nil {
		feed.place.Store(place)
	}
}

// stop closes the runtime's feed and removes its socket and directory.
func (g *gnssFeeds) stop(runtimeID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	feed := g.feeds[runtimeID]
	delete(g.feeds, runtimeID)
	g.mu.Unlock()
	if feed == nil {
		return
	}
	feed.place.Store(nil)
	_ = feed.listener.Close()
	<-feed.done
	if err := os.RemoveAll(feed.dir); err != nil {
		log.Printf("runtime %s: remove location feed: %v", runtimeID, err)
	}
}

func (g *gnssFeeds) serve(feed *gnssFeed) {
	defer close(feed.done)
	var handlers sync.WaitGroup
	defer handlers.Wait()
	for {
		conn, err := feed.listener.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		select {
		case feed.clients <- struct{}{}:
		default:
			// Every handler is busy with a client that sends nothing.
			_ = conn.Close()
			continue
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer func() { <-feed.clients }()
			g.answer(conn, feed.place.Load())
		}()
	}
}

// answer reads one command and, for a location request with a place set,
// writes one fix record. Anything else gets no data, which the HAL reads as
// no fix.
func (g *gnssFeeds) answer(conn *net.UnixConn, place *gnssPlace) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(gnssClientTimeout))
	command := make([]byte, 0, gnssMaxCommandBytes)
	buffer := make([]byte, gnssMaxCommandBytes)
	for len(command) < len(gnssGetLocation) {
		read, err := conn.Read(buffer[:gnssMaxCommandBytes-len(command)])
		command = append(command, buffer[:read]...)
		if err != nil || read == 0 || len(command) >= gnssMaxCommandBytes {
			break
		}
	}
	if string(command) != gnssGetLocation || place == nil {
		return
	}
	_, _ = io.WriteString(conn, gnssFixRecord(*place, g.now(), g.noise()))
}

// gnssNoise is how one fix differs from the exact place, as a real receiver's
// fixes wander: an offset in metres and the accuracy it claims.
type gnssNoise struct {
	northMeters, eastMeters float64
	accuracyMeters          float64
}

// realisticGNSSNoise wanders up to 3 m from the place and claims 3.5 to 8 m
// accuracy, as an open-sky phone fix does; a fix that never moves would mark
// the runtime as not a phone.
func realisticGNSSNoise() gnssNoise {
	distance := 3 * math.Sqrt(rand.Float64())
	angle := 2 * math.Pi * rand.Float64()
	return gnssNoise{
		northMeters:    distance * math.Cos(angle),
		eastMeters:     distance * math.Sin(angle),
		accuracyMeters: 3.5 + 4.5*rand.Float64(),
	}
}

// gnssFixRecord is one fix in the format FixLocationParser reads:
// Fix,Provider,LatitudeDegrees,LongitudeDegrees,AltitudeMeters,SpeedMps,
// AccuracyMeters,BearingDegrees,UnixTimeMillis,SpeedAccuracyMps,
// BearingAccuracyDegrees,ElapsedRealtimeNanos, ended by the HAL's record
// terminator. The node knows no altitude, speed or bearing, so they are 0;
// the HAL stamps elapsed realtime itself, so the last field is 0.
func gnssFixRecord(place gnssPlace, now time.Time, noise gnssNoise) string {
	const metersPerDegree = 111_320.0
	latitude := place.latitude + noise.northMeters/metersPerDegree
	latitude = math.Max(-90, math.Min(90, latitude))
	longitude := place.longitude
	if scale := math.Cos(latitude * math.Pi / 180); scale > 1e-6 {
		longitude += noise.eastMeters / (metersPerDegree * scale)
	}
	if longitude > 180 {
		longitude -= 360
	} else if longitude < -180 {
		longitude += 360
	}
	return fmt.Sprintf("Fix,GPS,%.7f,%.7f,0.0,0.0,%.1f,0.0,%d,0.5,90.0,0%s",
		latitude, longitude, noise.accuracyMeters, now.UnixMilli(), gnssRecordEnd)
}
