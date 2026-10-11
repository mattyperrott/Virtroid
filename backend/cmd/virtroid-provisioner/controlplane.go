package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"virtroid/backend/internal/nodeauth"
)

// provisionerRequestID must match the control plane's fixed provisioner id.
const provisionerRequestID = "virtroid-provisioner"

// provisionedVM mirrors the control plane's record of one runtime VM.
type provisionedVM struct {
	ID            string  `json:"id"`
	AccountID     *string `json:"account_id"`
	Region        string  `json:"region"`
	State         string  `json:"state"`
	Desired       string  `json:"desired"`
	ItemHash      string  `json:"item_hash"`
	CRNHash       string  `json:"crn_hash"`
	PublicIP      string  `json:"public_ip"`
	NodePublicKey string  `json:"node_public_key"`
}

type vmReport struct {
	State         string `json:"state,omitempty"`
	ItemHash      string `json:"item_hash,omitempty"`
	CRNHash       string `json:"crn_hash,omitempty"`
	PublicIP      string `json:"public_ip,omitempty"`
	NodePublicKey string `json:"node_public_key,omitempty"`
	Failure       string `json:"failure,omitempty"`
}

type controlPlane interface {
	Work(ctx context.Context) ([]provisionedVM, error)
	Report(ctx context.Context, vmID string, report vmReport) error
}

type httpControlPlane struct {
	baseURL    string
	privateKey *ecdsa.PrivateKey
	client     *http.Client
}

func (c httpControlPlane) do(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if err := nodeauth.ApplySignedHeaders(req, c.privateKey, provisionerRequestID, body, "", "",
		strconv.FormatInt(time.Now().Unix(), 10), uuid.NewString()); err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: status=%d %s", method, path, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out != nil {
		return json.Unmarshal(payload, out)
	}
	return nil
}

func (c httpControlPlane) Work(ctx context.Context) ([]provisionedVM, error) {
	var work struct {
		VMs []provisionedVM `json:"vms"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/internal/provisioner/vms", nil, &work); err != nil {
		return nil, err
	}
	return work.VMs, nil
}

func (c httpControlPlane) Report(ctx context.Context, vmID string, report vmReport) error {
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/api/v1/internal/provisioner/vms/"+url.PathEscape(vmID)+"/report", body, nil)
}
