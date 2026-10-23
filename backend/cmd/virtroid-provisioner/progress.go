package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// progressReader describes what a VM that is coming up is doing, from the
// compute node's own view. It is only shown to the waiting user, so a failure
// to read it is never an error.
type progressReader interface {
	Progress(ctx context.Context, itemHash string) string
}

type alephProgress struct {
	schedulerURL string
	client       *http.Client
	mu           sync.Mutex
	nodeURLs     map[string]string
}

func newAlephProgress(schedulerURL string) *alephProgress {
	return &alephProgress{
		schedulerURL: strings.TrimRight(schedulerURL, "/"),
		client:       &http.Client{Timeout: 10 * time.Second},
		nodeURLs:     map[string]string{},
	}
}

func (p *alephProgress) getJSON(ctx context.Context, target string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

func (p *alephProgress) nodeURL(ctx context.Context, itemHash string) string {
	p.mu.Lock()
	cached := p.nodeURLs[itemHash]
	p.mu.Unlock()
	if cached != "" {
		return cached
	}
	var allocation struct {
		Node struct {
			URL string `json:"url"`
		} `json:"node"`
	}
	if err := p.getJSON(ctx, p.schedulerURL+"/api/v0/allocation/"+url.PathEscape(itemHash), &allocation); err != nil {
		return ""
	}
	nodeURL := strings.TrimRight(allocation.Node.URL, "/")
	if parsed, err := url.Parse(nodeURL); err != nil || parsed.Scheme != "https" {
		return ""
	}
	p.mu.Lock()
	p.nodeURLs[itemHash] = nodeURL
	p.mu.Unlock()
	return nodeURL
}

func (p *alephProgress) Progress(ctx context.Context, itemHash string) string {
	if p.nodeURL(ctx, itemHash) == "" {
		return "Finding a machine for it"
	}
	execution, ok := p.execution(ctx, itemHash)
	if !ok {
		return "Waiting for the machine to take it"
	}
	return execution.describe()
}

// execution is the compute node's record of the VM, if it has one.
func (p *alephProgress) execution(ctx context.Context, itemHash string) (crnExecution, bool) {
	nodeURL := p.nodeURL(ctx, itemHash)
	if nodeURL == "" {
		return crnExecution{}, false
	}
	var executions map[string]crnExecution
	if err := p.getJSON(ctx, nodeURL+"/v2/about/executions/list", &executions); err != nil {
		return crnExecution{}, false
	}
	execution, ok := executions[itemHash]
	return execution, ok
}

type crnExecution struct {
	Running    bool `json:"running"`
	Allocation struct {
		State    string  `json:"state"`
		Attempts int     `json:"attempts"`
		Error    *string `json:"error"`
	} `json:"allocation"`
	Status struct {
		PreparingAt *string `json:"preparing_at"`
		PreparedAt  *string `json:"prepared_at"`
		StartingAt  *string `json:"starting_at"`
		StartedAt   *string `json:"started_at"`
	} `json:"status"`
	Networking struct {
		HostIPv4    string `json:"host_ipv4"`
		MappedPorts map[string]struct {
			Host int `json:"host"`
		} `json:"mapped_ports"`
	} `json:"networking"`
}

func (e crnExecution) describe() string {
	switch {
	case e.Allocation.Error != nil && *e.Allocation.Error != "":
		message := *e.Allocation.Error
		if len(message) > 120 {
			message = message[:120]
		}
		return fmt.Sprintf("The machine hit a problem; retrying (attempt %d): %s", e.Allocation.Attempts+1, message)
	case e.Running || e.Status.StartedAt != nil:
		return "Booted; waiting for its network address"
	case e.Status.StartingAt != nil:
		return "Booting and checking the system image"
	case e.Status.PreparedAt != nil:
		return "Disks ready; booting"
	case e.Status.PreparingAt != nil:
		return "Preparing its disks"
	case strings.EqualFold(e.Allocation.State, "downloading"):
		return "Downloading the system image"
	case e.Allocation.State != "":
		return "The machine reports: " + strings.ToLower(e.Allocation.State)
	}
	return "Waiting for the machine"
}
