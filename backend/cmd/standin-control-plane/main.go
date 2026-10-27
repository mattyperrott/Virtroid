// Command standin-control-plane stands in for virtroidd in
// deploy/aleph/test/boot-test.sh's location check. It gives the node under
// test one runtime to run, with a region sealed as the phone seals it and the
// runtime's keys sealed to the node as the phone hands them over, so the node
// starts the runtime, opens the region and serves its place as it would for
// an owner. Test only: it checks none of the node's signatures and keeps
// everything in memory.
//
//	standin-control-plane -listen 10.0.2.2:18080 -node-key KEY -node-id ID -runtime ID -image IMAGE
//
// The test drives it with GET and POST alone, as busybox's wget can: POST
// /test/run hands the runtime to the node (until then it has none), POST
// /test/region with {"latitude":,"longitude":,"timezone":} seals a region for
// it, POST /test/region/remove takes the region away, and GET /test/runtime
// returns what the node last reported ("status", "connection") and its logs,
// a line each.
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// These mirror the node's and the phone's (backend/cmd/virtnoded blobstore.go,
// blobkey_envelope.go, region.go): should they drift, the node refuses the
// keys or the region and the boot test fails.
const (
	envelopeAlgorithm   = "P256_ECDH_HKDF_SHA256_AESGCM_V1"
	envelopeHKDFInfo    = "virtroid-blob-key-envelope-v1"
	snapshotKeyContext  = "VIRTROID-RUNTIME-SNAPSHOT-KEY-V2"
	viewerKeyContext    = "VIRTROID-RUNTIME-VIEWER-KEY-V1"
	regionKeyContext    = "VIRTROID-RUNTIME-REGION-KEY-V1"
	sealedRegionMagic   = "VRTREG1\n"
	blobVerifierContext = "virtroid-blob-verifier-v1:"
)

type standIn struct {
	nodeID, runtimeID, image string
	nodeKey                  *ecdh.PublicKey
	// accountKey is the runtime's key material, as an account key from an
	// app that predates per-runtime keys: the node takes it from an
	// envelope sealed to its own key.
	accountKey []byte

	mu      sync.Mutex
	running bool
	region  *string
	status  map[string]any
	logs    []string
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18080", "address to listen on")
	nodeKey := flag.String("node-key", "", "the node's private key (base64 PKCS#8 DER), as boot-test.sh injects it")
	nodeID := flag.String("node-id", "smoke-node", "the node's id")
	runtimeID := flag.String("runtime", "", "the runtime's id")
	image := flag.String("image", "", "the runtime image the node is to run")
	flag.Parse()

	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*nodeKey))
	if err != nil {
		log.Fatalf("node key: %v", err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		log.Fatalf("node key: %v", err)
	}
	private, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		log.Fatal("node key is not an EC key")
	}
	public, err := private.PublicKey.ECDH()
	if err != nil {
		log.Fatalf("node key: %v", err)
	}
	if *runtimeID == "" || *image == "" {
		log.Fatal("-runtime and -image are required")
	}
	s := &standIn{nodeID: *nodeID, runtimeID: *runtimeID, image: *image, nodeKey: public,
		accountKey: make([]byte, 32), status: map[string]any{}}
	_, _ = rand.Read(s.accountKey)

	log.Printf("standing in for the control plane on %s for node %s, runtime %s", *listen, *nodeID, *runtimeID)
	log.Fatal(http.ListenAndServe(*listen, s))
}

func (s *standIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	runtimePath := "/api/v1/internal/runtimes/" + s.runtimeID + "/"
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/internal/hosts/heartbeat":
		writeJSON(w, map[string]any{})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/internal/hosts/"+s.nodeID+"/assignments":
		s.mu.Lock()
		running := s.running
		s.mu.Unlock()
		items := []any{}
		if running {
			items = append(items, s.assignment())
		}
		writeJSON(w, map[string]any{"items": items})
	case r.Method == http.MethodGet && r.URL.Path == runtimePath+"blob-key":
		envelope, err := s.envelope()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		digest := sha256.Sum256(append([]byte(blobVerifierContext), s.accountKey...))
		writeJSON(w, map[string]any{
			"blob_key_envelope":   envelope,
			"blob_key_verifier":   base64.RawURLEncoding.EncodeToString(digest[:]),
			"blob_key_expires_at": time.Now().UTC().Add(time.Hour),
		})
	case r.Method == http.MethodPost && r.URL.Path == runtimePath+"status":
		var body map[string]any
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		for key, value := range body {
			s.status[key] = value
		}
		s.mu.Unlock()
		log.Printf("status %v %v", body["status"], body["connection_status"])
		writeJSON(w, map[string]any{})
	case r.Method == http.MethodPost && r.URL.Path == runtimePath+"logs":
		var body struct {
			Level, Message string
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
		line := body.Level + ": " + body.Message
		s.mu.Lock()
		s.logs = append(s.logs, line)
		s.mu.Unlock()
		log.Printf("runtime log %s", line)
		writeJSON(w, map[string]any{})
	case r.Method == http.MethodPost && r.URL.Path == "/test/run":
		s.mu.Lock()
		s.running = true
		s.mu.Unlock()
		log.Printf("runtime handed to the node")
		writeJSON(w, map[string]any{})
	case r.Method == http.MethodPost && r.URL.Path == "/test/region":
		var region struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
			Timezone  string  `json:"timezone"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&region); err != nil ||
			math.Abs(region.Latitude) > 90 || math.Abs(region.Longitude) > 180 || region.Timezone == "" {
			http.Error(w, "a region is latitude, longitude and timezone", http.StatusBadRequest)
			return
		}
		sealed, err := s.sealRegion(region.Latitude, region.Longitude, region.Timezone)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.mu.Lock()
		s.region = &sealed
		s.mu.Unlock()
		log.Printf("region set")
		writeJSON(w, map[string]any{})
	case r.Method == http.MethodPost && r.URL.Path == "/test/region/remove":
		s.mu.Lock()
		s.region = nil
		s.mu.Unlock()
		log.Printf("region removed")
		writeJSON(w, map[string]any{})
	case r.Method == http.MethodGet && r.URL.Path == "/test/runtime":
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "status %v\nconnection %v\n", s.status["status"], s.status["connection_status"])
		for _, line := range s.logs {
			fmt.Fprintf(w, "log %s\n", line)
		}
	default:
		// Whatever else the node asks for (apps, notification keys) it
		// does without.
		log.Printf("not here: %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// assignment is the runtime as the control plane hands it to its node:
// running, on direct egress, with the region the test last set.
func (s *standIn) assignment() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	status, _ := s.status["status"].(string)
	connection, _ := s.status["connection_status"].(string)
	if status == "" {
		status, connection = "provisioning", "offline"
	}
	viewerPort := 17001
	return map[string]any{
		"id":                s.runtimeID,
		"name":              "boot test",
		"status":            status,
		"desired_state":     "running",
		"connection_status": connection,
		"persona_version":   1,
		"android_image":     s.image,
		"android_version":   "android-16",
		"width_px":          720,
		"height_px":         1280,
		"density_dpi":       320,
		"camera_mode":       "off",
		"file_mode":         "off",
		"viewer_port":       viewerPort,
		"egress_mode":       "direct",
		"region_sealed":     s.region,
		"selected_apps":     []any{},
	}
}

// envelope seals the account key to the node's key, as an app does.
func (s *standIn) envelope() (map[string]any, error) {
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	secret, err := ephemeral.ECDH(s.nodeKey)
	if err != nil {
		return nil, err
	}
	public, err := x509.MarshalPKIXPublicKey(ephemeral.PublicKey())
	if err != nil {
		return nil, err
	}
	leaseID := fmt.Sprintf("boot-test-%d", time.Now().UnixNano())
	aad := strings.Join([]string{"VIRTROID-BLOB-KEY-ENVELOPE-V1", leaseID, "start", s.runtimeID, s.nodeID}, "\n")
	salt := sha256.Sum256([]byte(aad))
	key := hkdf(secret, salt[:], []byte(envelopeHKDFInfo), 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, aead.NonceSize())
	_, _ = rand.Read(iv)
	return map[string]any{
		"version":              1,
		"algorithm":            envelopeAlgorithm,
		"lease_id":             leaseID,
		"operation":            "start",
		"runtime_id":           s.runtimeID,
		"host_id":              s.nodeID,
		"ephemeral_public_key": base64.StdEncoding.EncodeToString(public),
		"iv":                   base64.StdEncoding.EncodeToString(iv),
		"ciphertext":           base64.StdEncoding.EncodeToString(aead.Seal(nil, iv, s.accountKey, []byte(aad))),
	}, nil
}

// sealRegion seals a region for the runtime as the phone does (RegionSeal.kt):
// with the key derived from the runtime's viewer key.
func (s *standIn) sealRegion(latitude, longitude float64, timezone string) (string, error) {
	snapshot := mac(s.accountKey, []byte(snapshotKeyContext), []byte{0}, []byte(s.runtimeID))
	viewer := mac(snapshot, []byte(viewerKeyContext))
	key := mac(viewer, []byte(regionKeyContext))
	plaintext, err := json.Marshal(map[string]any{"version": 1, "latitude": latitude, "longitude": longitude, "timezone": timezone})
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	_, _ = rand.Read(nonce)
	sealed := append([]byte(sealedRegionMagic), nonce...)
	sealed = aead.Seal(sealed, nonce, plaintext, []byte("virtroid-region-v1\n"+s.runtimeID))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func mac(key []byte, parts ...[]byte) []byte {
	h := hmac.New(sha256.New, key)
	for _, part := range parts {
		h.Write(part)
	}
	return h.Sum(nil)
}

func hkdf(secret, salt, info []byte, length int) []byte {
	prk := mac(salt, secret)
	var out, previous []byte
	for counter := byte(1); len(out) < length; counter++ {
		previous = mac(prk, previous, info, []byte{counter})
		out = append(out, previous...)
	}
	return out[:length]
}
