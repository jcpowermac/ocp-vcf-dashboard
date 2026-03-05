package server

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"k8s.io/klog/v2"

	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/store"
)

// handleSSE handles Server-Sent Event connections for real-time updates.
// Clients connect to /api/sse/{resource} where resource is one of:
// overview, pools, leases, networks, prow, vcenter
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	resource := strings.TrimPrefix(r.URL.Path, "/api/sse/")

	var eventType store.EventType
	switch resource {
	case "overview":
		eventType = store.EventOverview
	case "pools":
		eventType = store.EventPools
	case "leases":
		eventType = store.EventLeases
	case "networks":
		eventType = store.EventNetworks
	case "prow":
		eventType = store.EventProw
	case "vcenter":
		eventType = store.EventVCenter
	default:
		http.NotFound(w, r)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sub := s.store.Subscribe()
	defer s.store.Unsubscribe(sub)

	ctx := r.Context()

	// Send initial data
	s.sendSSEFragment(w, flusher, resource)

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-sub:
			if !ok {
				return
			}
			if evt == eventType || evt == store.EventOverview && resource == "overview" {
				s.sendSSEFragment(w, flusher, resource)
			}
		}
	}
}

func (s *Server) sendSSEFragment(w http.ResponseWriter, flusher http.Flusher, resource string) {
	templateName := resource + "-content"

	var data interface{}
	switch resource {
	case "overview":
		data = map[string]interface{}{
			"Overview": s.store.GetOverview(),
		}
	case "pools":
		data = map[string]interface{}{
			"Pools":  s.store.GetPools(),
			"Leases": s.store.GetLeases(),
		}
	case "leases":
		data = map[string]interface{}{
			"Leases": s.store.GetLeases(),
		}
	case "networks":
		data = map[string]interface{}{
			"Networks": s.store.GetNetworks(),
			"Leases":   s.store.GetLeases(),
		}
	case "prow":
		jobs, updated := s.store.GetProwJobs()
		data = map[string]interface{}{
			"ProwJobs":    jobs,
			"ProwUpdated": updated,
			"Leases":      s.store.GetLeases(),
		}
	case "vcenter":
		vcData, updated := s.store.GetVCenterData()
		data = map[string]interface{}{
			"VCenterData":    vcData,
			"VCenterUpdated": updated,
		}
	}

	var buf bytes.Buffer
	if err := s.templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		klog.Errorf("SSE template error for %s: %v", templateName, err)
		return
	}

	// SSE format: event name, then data lines, then blank line
	fmt.Fprintf(w, "event: %s\n", resource)
	for _, line := range strings.Split(buf.String(), "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprintf(w, "\n")
	flusher.Flush()
}
