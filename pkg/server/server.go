// Package server provides the HTTP server for the dashboard web UI.
package server

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	vcmv1 "github.com/openshift-splat-team/vsphere-capacity-manager/pkg/apis/vspherecapacitymanager.splat.io/v1"
	"k8s.io/klog/v2"

	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/prow"
	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/store"
	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/vcenter"
)

//go:embed views/*.html
var viewsFS embed.FS

// Server holds the HTTP server state.
type Server struct {
	store      *store.Store
	templates  *template.Template
	mux        *http.ServeMux
	consoleURL string
}

// New creates a new dashboard HTTP server.
func New(s *store.Store, consoleURL string) (*Server, error) {
	funcMap := template.FuncMap{
		"pct": func(used, total int) float64 {
			if total == 0 {
				return 0
			}
			return float64(used) / float64(total) * 100
		},
		"pctf": func(used, total float64) float64 {
			if total == 0 {
				return 0
			}
			return used / total * 100
		},
		"sub": func(a, b int) int { return a - b },
		"timeAgo": func(t time.Time) string {
			if t.IsZero() {
				return "never"
			}
			d := time.Since(t)
			if d < time.Minute {
				return "just now"
			}
			if d < time.Hour {
				return strings.TrimSuffix(d.Truncate(time.Minute).String(), "0s")
			}
			if d < 24*time.Hour {
				return strings.TrimSuffix(d.Truncate(time.Hour).String(), "0m0s")
			}
			return strings.TrimSuffix(d.Truncate(24*time.Hour).String(), "0h0m0s")
		},
		"fmtPct": func(f float64) string {
			return strings.TrimRight(strings.TrimRight(
				strings.Replace(
					template.JSEscapeString(
						strings.TrimRight(strings.TrimRight(
							func() string { return template.HTMLEscapeString("") }(), "0"), ".")),
					"", "", 0),
				"0"), ".")
		},
		"poolServer": func(p *vcmv1.Pool) string {
			return p.Spec.Server
		},
		"leasePoolNames": func(l *vcmv1.Lease) []string {
			var names []string
			for _, ref := range l.OwnerReferences {
				if ref.Kind == "Pool" {
					names = append(names, ref.Name)
				}
			}
			return names
		},
		"leaseNetworkNames": func(l *vcmv1.Lease) []string {
			var names []string
			for _, ref := range l.OwnerReferences {
				if ref.Kind == "Network" {
					names = append(names, ref.Name)
				}
			}
			return names
		},
		"sparkClass": func(c string) string {
			switch c {
			case "S":
				return "spark-success"
			case "F":
				return "spark-failure"
			case "P":
				return "spark-pending"
			case "A", "E":
				return "spark-error"
			default:
				return "spark-unknown"
			}
		},
		"splitSparkline": func(s string) []string {
			chars := make([]string, len(s))
			for i, c := range s {
				chars[i] = string(c)
			}
			return chars
		},
		"statusClass": func(phase vcmv1.Phase) string {
			switch phase {
			case vcmv1.PHASE_FULFILLED:
				return "status-fulfilled"
			case vcmv1.PHASE_PENDING:
				return "status-pending"
			case vcmv1.PHASE_FAILED:
				return "status-failed"
			default:
				return "status-unknown"
			}
		},
		"prowStateClass": func(state string) string {
			switch state {
			case "success":
				return "status-fulfilled"
			case "failure":
				return "status-failed"
			case "pending":
				return "status-pending"
			default:
				return "status-unknown"
			}
		},
		"sortPools": func(pools []*vcmv1.Pool) []*vcmv1.Pool {
			sort.Slice(pools, func(i, j int) bool {
				return pools[i].Name < pools[j].Name
			})
			return pools
		},
		"sortLeases": func(leases []*vcmv1.Lease) []*vcmv1.Lease {
			sort.Slice(leases, func(i, j int) bool {
				return leases[i].Name < leases[j].Name
			})
			return leases
		},
		"sortNetworks": func(networks []*vcmv1.Network) []*vcmv1.Network {
			sort.Slice(networks, func(i, j int) bool {
				return networks[i].Name < networks[j].Name
			})
			return networks
		},
		"fmtFloat1": func(f float64) string {
			s := strings.TrimRight(strings.TrimRight(
				func() string {
					r := []byte{}
					r = append(r, []byte(func() string {
						if f < 0 {
							return "-"
						}
						return ""
					}())...)
					abs := f
					if abs < 0 {
						abs = -abs
					}
					whole := int64(abs)
					frac := int64((abs - float64(whole)) * 10)
					for _, b := range []byte(func() string {
						s := ""
						for whole > 0 {
							s = string(rune('0'+whole%10)) + s
							whole /= 10
						}
						if s == "" {
							s = "0"
						}
						return s
					}()) {
						r = append(r, b)
					}
					r = append(r, '.')
					r = append(r, byte('0'+frac))
					return string(r)
				}(),
				"0"), ".")
			return s
		},
		"poolLeaseCount": func(poolName string, leases []*vcmv1.Lease) int {
			count := 0
			for _, l := range leases {
				for _, ref := range l.OwnerReferences {
					if ref.Kind == "Pool" && ref.Name == poolName {
						count++
						break
					}
				}
			}
			return count
		},
		"prowJobLeases": func(jobName string, leases []*vcmv1.Lease) []*vcmv1.Lease {
			var result []*vcmv1.Lease
			for _, l := range leases {
				if l.Annotations != nil {
					if val, ok := l.Annotations["prow-job-name"]; ok && val == jobName {
						result = append(result, l)
					}
				}
			}
			return result
		},
		"networkOwnerLease": func(n *vcmv1.Network, leases []*vcmv1.Lease) string {
			for _, l := range leases {
				for _, ref := range l.OwnerReferences {
					if ref.Kind == "Network" && ref.Name == n.Name {
						return l.Name
					}
				}
			}
			return ""
		},
		"bytesToGB": func(b int64) float64 {
			return float64(b) / (1024 * 1024 * 1024)
		},
		"mul": func(a, b float64) float64 { return a * b },
		"div": func(a, b float64) float64 {
			if b == 0 {
				return 0
			}
			return a / b
		},
		"consoleNSURL": func(ns string) string {
			if consoleURL == "" || ns == "" {
				return ""
			}
			return strings.TrimRight(consoleURL, "/") + "/k8s/ns/" + ns + "/core~v1~Pod"
		},
	}

	tmpl, err := template.New("").Funcs(funcMap).ParseFS(viewsFS, "views/*.html")
	if err != nil {
		return nil, err
	}

	srv := &Server{
		store:      s,
		templates:  tmpl,
		mux:        http.NewServeMux(),
		consoleURL: consoleURL,
	}

	srv.registerRoutes()
	return srv, nil
}

func (s *Server) registerRoutes() {
	// Page routes
	s.mux.HandleFunc("/", s.handleOverview)
	s.mux.HandleFunc("/pools", s.handlePools)
	s.mux.HandleFunc("/leases", s.handleLeases)
	s.mux.HandleFunc("/networks", s.handleNetworks)
	s.mux.HandleFunc("/prow", s.handleProw)
	s.mux.HandleFunc("/vcenter", s.handleVCenter)

	// Detail page routes
	s.mux.HandleFunc("/leases/", s.handleLeaseDetail)
	s.mux.HandleFunc("/pools/", s.handlePoolDetail)
	s.mux.HandleFunc("/cluster/", s.handleClusterDetail)
	s.mux.HandleFunc("/prow/", s.handleProwDetail)

	// SSE endpoints
	s.mux.HandleFunc("/api/sse/", s.handleSSE)

	// Fragment endpoints for HTMX partial updates
	s.mux.HandleFunc("/fragments/overview", s.handleOverviewFragment)
	s.mux.HandleFunc("/fragments/pools", s.handlePoolsFragment)
	s.mux.HandleFunc("/fragments/leases", s.handleLeasesFragment)
	s.mux.HandleFunc("/fragments/networks", s.handleNetworksFragment)
	s.mux.HandleFunc("/fragments/prow", s.handleProwFragment)
	s.mux.HandleFunc("/fragments/vcenter", s.handleVCenterFragment)

	// Static files
	s.mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// Health endpoints
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
}

// Handler returns the HTTP handler for the server.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// --- Page Handlers ---

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data := map[string]interface{}{
		"Page":     "overview",
		"Overview": s.store.GetOverview(),
	}
	s.render(w, "layout.html", data)
}

func (s *Server) handlePools(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Page":   "pools",
		"Pools":  s.store.GetPools(),
		"Leases": s.store.GetLeases(),
	}
	s.render(w, "layout.html", data)
}

func (s *Server) handleLeases(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Page":   "leases",
		"Leases": s.store.GetLeases(),
	}
	s.render(w, "layout.html", data)
}

func (s *Server) handleNetworks(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Page":     "networks",
		"Networks": s.store.GetNetworks(),
		"Leases":   s.store.GetLeases(),
	}
	s.render(w, "layout.html", data)
}

func (s *Server) handleProw(w http.ResponseWriter, r *http.Request) {
	jobs, updated := s.store.GetProwJobs()
	data := map[string]interface{}{
		"Page":        "prow",
		"ProwJobs":    jobs,
		"ProwUpdated": updated,
		"Leases":      s.store.GetLeases(),
	}
	s.render(w, "layout.html", data)
}

func (s *Server) handleVCenter(w http.ResponseWriter, r *http.Request) {
	vcData, updated := s.store.GetVCenterData()
	data := map[string]interface{}{
		"Page":           "vcenter",
		"VCenterData":    vcData,
		"VCenterUpdated": updated,
	}
	s.render(w, "layout.html", data)
}

// --- Fragment Handlers (for HTMX partial updates) ---

func (s *Server) handleOverviewFragment(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Overview": s.store.GetOverview(),
	}
	s.renderFragment(w, "overview-content", data)
}

func (s *Server) handlePoolsFragment(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Pools":  s.store.GetPools(),
		"Leases": s.store.GetLeases(),
	}
	s.renderFragment(w, "pools-content", data)
}

func (s *Server) handleLeasesFragment(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Leases": s.store.GetLeases(),
	}
	s.renderFragment(w, "leases-content", data)
}

func (s *Server) handleNetworksFragment(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Networks": s.store.GetNetworks(),
		"Leases":   s.store.GetLeases(),
	}
	s.renderFragment(w, "networks-content", data)
}

func (s *Server) handleProwFragment(w http.ResponseWriter, r *http.Request) {
	jobs, updated := s.store.GetProwJobs()
	data := map[string]interface{}{
		"ProwJobs":    jobs,
		"ProwUpdated": updated,
		"Leases":      s.store.GetLeases(),
	}
	s.renderFragment(w, "prow-content", data)
}

func (s *Server) handleVCenterFragment(w http.ResponseWriter, r *http.Request) {
	vcData, updated := s.store.GetVCenterData()
	data := map[string]interface{}{
		"VCenterData":    vcData,
		"VCenterUpdated": updated,
	}
	s.renderFragment(w, "vcenter-content", data)
}

// --- Detail Page Handlers ---

func (s *Server) handleLeaseDetail(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/leases/")
	if name == "" {
		http.Redirect(w, r, "/leases", http.StatusFound)
		return
	}

	lease := s.store.GetLease(name)
	if lease == nil {
		http.NotFound(w, r)
		return
	}

	// Find associated pools via OwnerReferences
	var pools []*vcmv1.Pool
	for _, ref := range lease.OwnerReferences {
		if ref.Kind == "Pool" {
			if p := s.store.GetPool(ref.Name); p != nil {
				pools = append(pools, p)
			}
		}
	}

	// Find associated VMs via lease-namespace label -> VM Namespace
	var vms []vcenter.VMWithServer
	if lease.Labels != nil {
		if ns, ok := lease.Labels[vcmv1.LeaseNamespace]; ok && ns != "" {
			vms = s.store.GetVMsByNamespace(ns)
		}
	}

	// Find associated Prow job via prow-job-name annotation (full Prow job name)
	var prowJob *prow.JobSummary
	if lease.Annotations != nil {
		if jobName, ok := lease.Annotations["prow-job-name"]; ok && jobName != "" {
			prowJob = s.store.GetProwJobSummary(jobName)
		}
	}

	data := map[string]interface{}{
		"Page":    "lease-detail",
		"Lease":   lease,
		"Pools":   pools,
		"VMs":     vms,
		"ProwJob": prowJob,
	}
	s.render(w, "layout.html", data)
}

func (s *Server) handlePoolDetail(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/pools/")
	if name == "" {
		http.Redirect(w, r, "/pools", http.StatusFound)
		return
	}

	pool := s.store.GetPool(name)
	if pool == nil {
		http.NotFound(w, r)
		return
	}

	// Find leases assigned to this pool via OwnerReferences
	allLeases := s.store.GetLeases()
	var leases []*vcmv1.Lease
	for _, l := range allLeases {
		for _, ref := range l.OwnerReferences {
			if ref.Kind == "Pool" && ref.Name == name {
				leases = append(leases, l)
				break
			}
		}
	}

	data := map[string]interface{}{
		"Page":   "pool-detail",
		"Pool":   pool,
		"Leases": leases,
	}
	s.render(w, "layout.html", data)
}

func (s *Server) handleClusterDetail(w http.ResponseWriter, r *http.Request) {
	ns := strings.TrimPrefix(r.URL.Path, "/cluster/")
	if ns == "" {
		http.Redirect(w, r, "/vcenter", http.StatusFound)
		return
	}

	// Find VMs in this namespace
	vms := s.store.GetVMsByNamespace(ns)

	// Find associated lease via lease-namespace label
	lease := s.store.GetLeaseByNamespace(ns)

	// Return 404 only when we have neither VMs nor a lease
	if len(vms) == 0 && lease == nil {
		http.NotFound(w, r)
		return
	}

	// If we have a lease, find associated pools and Prow job
	var pools []*vcmv1.Pool
	var prowJob *prow.JobSummary
	if lease != nil {
		for _, ref := range lease.OwnerReferences {
			if ref.Kind == "Pool" {
				if p := s.store.GetPool(ref.Name); p != nil {
					pools = append(pools, p)
				}
			}
		}
		if lease.Annotations != nil {
			if jobName, ok := lease.Annotations["prow-job-name"]; ok && jobName != "" {
				prowJob = s.store.GetProwJobSummary(jobName)
			}
		}
	}

	data := map[string]interface{}{
		"Page":      "cluster-detail",
		"Namespace": ns,
		"VMs":       vms,
		"Lease":     lease,
		"Pools":     pools,
		"ProwJob":   prowJob,
	}
	s.render(w, "layout.html", data)
}

func (s *Server) handleProwDetail(w http.ResponseWriter, r *http.Request) {
	jobName := strings.TrimPrefix(r.URL.Path, "/prow/")
	if jobName == "" {
		http.Redirect(w, r, "/prow", http.StatusFound)
		return
	}

	prowJob := s.store.GetProwJobSummary(jobName)
	if prowJob == nil {
		http.NotFound(w, r)
		return
	}

	// Find leases associated with this Prow job via the "job-name" label
	leases := s.store.GetLeasesByJobName(jobName)

	// Collect VMs and pools from all associated leases
	var vms []vcenter.VMWithServer
	poolMap := make(map[string]*vcmv1.Pool)
	for _, l := range leases {
		// Gather VMs via lease-namespace label -> VM Namespace
		if l.Labels != nil {
			if ns, ok := l.Labels[vcmv1.LeaseNamespace]; ok && ns != "" {
				vms = append(vms, s.store.GetVMsByNamespace(ns)...)
			}
		}
		// Gather pools via OwnerReferences
		for _, ref := range l.OwnerReferences {
			if ref.Kind == "Pool" {
				if _, exists := poolMap[ref.Name]; !exists {
					if p := s.store.GetPool(ref.Name); p != nil {
						poolMap[ref.Name] = p
					}
				}
			}
		}
	}

	var pools []*vcmv1.Pool
	for _, p := range poolMap {
		pools = append(pools, p)
	}

	data := map[string]interface{}{
		"Page":    "prow-detail",
		"ProwJob": prowJob,
		"Leases":  leases,
		"VMs":     vms,
		"Pools":   pools,
	}
	s.render(w, "layout.html", data)
}

func (s *Server) render(w http.ResponseWriter, name string, data interface{}) {
	var buf bytes.Buffer
	if err := s.templates.ExecuteTemplate(&buf, name, data); err != nil {
		klog.Errorf("Template render error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

func (s *Server) renderFragment(w http.ResponseWriter, name string, data interface{}) {
	var buf bytes.Buffer
	if err := s.templates.ExecuteTemplate(&buf, name, data); err != nil {
		klog.Errorf("Fragment render error for %s: %v", name, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}
