package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"virtroid/backend/internal/config"
	"virtroid/backend/internal/nodeauth"
	"virtroid/backend/internal/store"
)

func TestProductionNodeRegistryRejectsTOFUAndHonorsRotationAndRevocation(t *testing.T) {
	databaseURL := os.Getenv("VIRTROID_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VIRTROID_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	registry, err := store.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer registry.Close()
	if err := registry.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	nodeID := "http-node-" + uuid.NewString()
	operatorID := "http-operator-" + uuid.NewString()
	cleanupNodeRegistryFixture(t, databaseURL, nodeID, operatorID)
	privateV1, publicV1 := integrationNodeKeyPair(t)
	privateV2, publicV2 := integrationNodeKeyPair(t)
	roguePrivate, roguePublic := integrationNodeKeyPair(t)
	heartbeatBody := []byte(fmt.Sprintf(
		`{"id":%q,"name":"HTTP registry node","advertise_addr":"virtnoded","relay_port":8090,"docker_socket":true,"binder":true,"blob_store_kind":"local-disk"}`,
		nodeID,
	))
	handler := New(config.ServerConfig{
		AppEnv:                           "production",
		NodeRegistrationSecret:           "legacy-registration-secret",
		NodeDevelopmentEnrollmentEnabled: true,
		NodeAllowedAdvertiseAddrs:        []string{"virtnoded"},
	}, registry)

	// A valid signature plus the old shared registration secret must not create
	// trust in production.
	resp := serveSignedNodeRequest(
		t,
		handler,
		privateV1,
		publicV1,
		nodeID,
		http.MethodPost,
		"/api/v1/internal/hosts/heartbeat",
		heartbeatBody,
		"legacy-registration-secret",
	)
	if resp.Code != http.StatusUnauthorized || !strings.Contains(resp.Body.String(), "node is not approved") {
		t.Fatalf("unknown production node response = %d %s", resp.Code, resp.Body.String())
	}
	if _, err := registry.ApprovedNode(ctx, nodeID); err != store.ErrApprovedNodeNotFound {
		t.Fatalf("unknown request created registry state: %v", err)
	}

	approved, err := registry.ApproveNode(ctx, store.ApproveNodeInput{
		NodeID:       nodeID,
		OperatorID:   operatorID,
		OperatorName: "HTTP integration operator",
		PublicKey:    publicV1,
		Actor:        "http-integration-test",
		Reason:       "approve fixture",
	})
	if err != nil {
		t.Fatalf("approve node: %v", err)
	}
	resp = serveSignedNodeRequest(
		t,
		handler,
		privateV1,
		publicV1,
		nodeID,
		http.MethodPost,
		"/api/v1/internal/hosts/heartbeat",
		heartbeatBody,
		"",
	)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("approved heartbeat response = %d %s", resp.Code, resp.Body.String())
	}

	resp = serveSignedNodeRequest(
		t,
		handler,
		roguePrivate,
		roguePublic,
		nodeID,
		http.MethodPost,
		"/api/v1/internal/hosts/heartbeat",
		heartbeatBody,
		"legacy-registration-secret",
	)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("rogue replacement response = %d %s", resp.Code, resp.Body.String())
	}
	afterRogue, err := registry.ApprovedNode(ctx, nodeID)
	if err != nil {
		t.Fatalf("read node after rogue request: %v", err)
	}
	if afterRogue.ActiveKeyVersion != 1 || afterRogue.Keys[0].FingerprintSHA256 != approved.Keys[0].FingerprintSHA256 {
		t.Fatalf("rogue request mutated registry: %+v", afterRogue)
	}

	if _, err := registry.RotateNodeKey(ctx, store.RotateNodeKeyInput{
		NodeID:    nodeID,
		PublicKey: publicV2,
		Overlap:   time.Minute,
		Actor:     "http-integration-test",
		Reason:    "rotation fixture",
	}); err != nil {
		t.Fatalf("rotate key: %v", err)
	}
	resp = serveSignedNodeRequest(
		t,
		handler,
		privateV1,
		publicV1,
		nodeID,
		http.MethodGet,
		"/api/v1/internal/hosts/"+nodeID+"/assignments",
		nil,
		"",
	)
	if resp.Code != http.StatusOK {
		t.Fatalf("overlap-key request response = %d %s", resp.Code, resp.Body.String())
	}
	resp = serveSignedNodeRequest(
		t,
		handler,
		privateV2,
		publicV2,
		nodeID,
		http.MethodPost,
		"/api/v1/internal/hosts/heartbeat",
		heartbeatBody,
		"",
	)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("rotated-key heartbeat response = %d %s", resp.Code, resp.Body.String())
	}

	if _, err := registry.RevokeNode(ctx, store.RevokeNodeInput{
		NodeID: nodeID,
		Actor:  "http-integration-test",
		Reason: "revocation fixture",
	}); err != nil {
		t.Fatalf("revoke node: %v", err)
	}
	resp = serveSignedNodeRequest(
		t,
		handler,
		privateV2,
		publicV2,
		nodeID,
		http.MethodGet,
		"/api/v1/internal/hosts/"+nodeID+"/assignments",
		nil,
		"legacy-registration-secret",
	)
	if resp.Code != http.StatusUnauthorized || !strings.Contains(resp.Body.String(), "node is not approved") {
		t.Fatalf("revoked-node response = %d %s", resp.Code, resp.Body.String())
	}
}

func TestDevelopmentNodeEnrollmentWritesSeparateRegistry(t *testing.T) {
	databaseURL := os.Getenv("VIRTROID_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VIRTROID_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	registry, err := store.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer registry.Close()
	if err := registry.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	nodeID := "development-node-" + uuid.NewString()
	cleanupNodeRegistryFixture(t, databaseURL, nodeID, "")
	privateKey, publicKey := integrationNodeKeyPair(t)
	body := []byte(fmt.Sprintf(
		`{"id":%q,"name":"Development node","advertise_addr":"node.dev.internal","relay_port":8090,"docker_socket":true,"binder":true}`,
		nodeID,
	))
	handler := New(config.ServerConfig{
		AppEnv:                           "development",
		NodeDevelopmentEnrollmentEnabled: true,
	}, registry)
	resp := serveSignedNodeRequest(
		t,
		handler,
		privateKey,
		publicKey,
		nodeID,
		http.MethodPost,
		"/api/v1/internal/hosts/heartbeat",
		body,
		"",
	)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("development enrollment response = %d %s", resp.Code, resp.Body.String())
	}
	approved, err := registry.ApprovedNode(ctx, nodeID)
	if err != nil {
		t.Fatalf("read development registry entry: %v", err)
	}
	if approved.OperatorID != "development" || approved.Status != "approved" {
		t.Fatalf("development registry entry = %+v", approved)
	}
}

func integrationNodeKeyPair(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate node key: %v", err)
	}
	publicKey, err := nodeauth.PublicKeyMaterial(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("encode node public key: %v", err)
	}
	return privateKey, publicKey
}

func serveSignedNodeRequest(
	t *testing.T,
	handler http.Handler,
	privateKey *ecdsa.PrivateKey,
	publicKey string,
	nodeID string,
	method string,
	path string,
	body []byte,
	registrationSecret string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonce := uuid.NewString()
	bodyHash, signature, err := nodeauth.Sign(privateKey, method, req.URL.RequestURI(), nodeID, timestamp, nonce, body)
	if err != nil {
		t.Fatalf("sign node request: %v", err)
	}
	req.Header.Set(nodeauth.HeaderNodeID, nodeID)
	req.Header.Set(nodeauth.HeaderTimestamp, timestamp)
	req.Header.Set(nodeauth.HeaderNonce, nonce)
	req.Header.Set(nodeauth.HeaderBodySHA256, bodyHash)
	req.Header.Set(nodeauth.HeaderSignature, signature)
	req.Header.Set(nodeauth.HeaderPublicKey, publicKey)
	if registrationSecret != "" {
		req.Header.Set(nodeauth.HeaderRegistrationSecret, registrationSecret)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

func cleanupNodeRegistryFixture(t *testing.T, databaseURL, nodeID, operatorID string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open cleanup database: %v", err)
	}
	t.Cleanup(func() {
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = db.ExecContext(ctx, `DELETE FROM node_request_nonces WHERE node_id = $1`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM hosts WHERE id = $1`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM node_registry_audit WHERE node_id = $1`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM approved_nodes WHERE node_id = $1`, nodeID)
		if operatorID != "" {
			_, _ = db.ExecContext(ctx, `DELETE FROM operator_registry_audit WHERE operator_id = $1`, operatorID)
			_, _ = db.ExecContext(ctx, `DELETE FROM node_operators WHERE id = $1`, operatorID)
		}
	})
}
