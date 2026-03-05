// Package prow fetches and analyzes vSphere periodic Prow CI jobs.
// This is a Go port of the Python vsphere-prow-summary tool's
// fetcher.py and analyzer.py modules.
package prow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

const (
	// ProwAPIURL is the Prow API endpoint, stripping unnecessary fields.
	ProwAPIURL = "https://prow.ci.openshift.org/prowjobs.js?omit=annotations,decoration_config,pod_spec"

	// DefaultPollInterval is how often to re-fetch Prow data.
	DefaultPollInterval = 5 * time.Minute

	// httpTimeout for the Prow API request.
	httpTimeout = 120 * time.Second
)

// prowResponse is the top-level JSON structure from the Prow API.
type prowResponse struct {
	Items []prowItem `json:"items"`
}

// prowItem represents a single Prow job entry.
type prowItem struct {
	Spec   prowSpec   `json:"spec"`
	Status prowStatus `json:"status"`
}

type prowSpec struct {
	Type string `json:"type"`
	Job  string `json:"job"`
}

type prowStatus struct {
	State          string `json:"state"`
	StartTime      string `json:"startTime"`
	CompletionTime string `json:"completionTime"`
	URL            string `json:"url"`
	BuildID        string `json:"build_id"`
}

// Fetcher periodically fetches Prow job data and caches it in memory.
type Fetcher struct {
	mu       sync.RWMutex
	cache    *prowResponse
	cacheAge time.Time
	interval time.Duration
}

// NewFetcher creates a new Prow data fetcher.
func NewFetcher(interval time.Duration) *Fetcher {
	return &Fetcher{
		interval: interval,
	}
}

// FetchOnce performs a single fetch from the Prow API.
func (f *Fetcher) FetchOnce(ctx context.Context) ([]JobSummary, error) {
	data, err := f.fetchFromAPI(ctx)
	if err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.cache = data
	f.cacheAge = time.Now()
	f.mu.Unlock()

	return Analyze(data), nil
}

// Run starts the periodic fetch loop. It blocks until ctx is cancelled.
func (f *Fetcher) Run(ctx context.Context, callback func([]JobSummary)) {
	// Initial fetch
	jobs, err := f.FetchOnce(ctx)
	if err != nil {
		klog.Errorf("Initial prow fetch failed: %v", err)
	} else {
		callback(jobs)
		klog.Infof("Initial prow fetch: %d job summaries", len(jobs))
	}

	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			jobs, err := f.FetchOnce(ctx)
			if err != nil {
				klog.Errorf("Prow fetch failed: %v", err)
				continue
			}
			callback(jobs)
			klog.V(2).Infof("Prow refresh: %d job summaries", len(jobs))
		}
	}
}

func (f *Fetcher) fetchFromAPI(ctx context.Context) (*prowResponse, error) {
	reqCtx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, ProwAPIURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating prow request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching prow data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prow API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading prow response: %w", err)
	}

	var data prowResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("parsing prow JSON: %w", err)
	}

	return &data, nil
}
