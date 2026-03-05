// Package store provides a thread-safe in-memory data store for all
// dashboard data (CRDs, Prow jobs, vCenter metrics). It supports
// change notification via subscriber channels for SSE streaming.
package store

import (
	"sync"
	"time"

	vcmv1 "github.com/openshift-splat-team/vsphere-capacity-manager/pkg/apis/vspherecapacitymanager.splat.io/v1"

	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/prow"
	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/vcenter"
)

// EventType represents the kind of resource that changed.
type EventType string

const (
	EventPools    EventType = "pools"
	EventLeases   EventType = "leases"
	EventNetworks EventType = "networks"
	EventProw     EventType = "prow"
	EventVCenter  EventType = "vcenter"
	EventOverview EventType = "overview"
)

// Store holds all dashboard data in memory and notifies subscribers on changes.
type Store struct {
	mu sync.RWMutex

	pools    map[string]*vcmv1.Pool
	leases   map[string]*vcmv1.Lease
	networks map[string]*vcmv1.Network

	prowJobs    []prow.JobSummary
	prowUpdated time.Time

	vcenterData    map[string]*vcenter.VCenterData
	vcenterUpdated time.Time

	// subscribers receive event types when data changes
	subMu       sync.Mutex
	subscribers map[chan EventType]struct{}
}

// New creates a new empty Store.
func New() *Store {
	return &Store{
		pools:       make(map[string]*vcmv1.Pool),
		leases:      make(map[string]*vcmv1.Lease),
		networks:    make(map[string]*vcmv1.Network),
		vcenterData: make(map[string]*vcenter.VCenterData),
		subscribers: make(map[chan EventType]struct{}),
	}
}

// Subscribe returns a channel that receives event types on data changes.
// Call Unsubscribe when done.
func (s *Store) Subscribe() chan EventType {
	ch := make(chan EventType, 64)
	s.subMu.Lock()
	s.subscribers[ch] = struct{}{}
	s.subMu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel.
func (s *Store) Unsubscribe(ch chan EventType) {
	s.subMu.Lock()
	delete(s.subscribers, ch)
	s.subMu.Unlock()
	close(ch)
}

func (s *Store) notify(events ...EventType) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for ch := range s.subscribers {
		for _, evt := range events {
			select {
			case ch <- evt:
			default:
				// subscriber is slow, skip
			}
		}
	}
}

// --- Pool operations ---

func (s *Store) SetPool(pool *vcmv1.Pool) {
	s.mu.Lock()
	s.pools[pool.Name] = pool.DeepCopy()
	s.mu.Unlock()
	s.notify(EventPools, EventOverview)
}

func (s *Store) DeletePool(name string) {
	s.mu.Lock()
	delete(s.pools, name)
	s.mu.Unlock()
	s.notify(EventPools, EventOverview)
}

func (s *Store) GetPools() []*vcmv1.Pool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*vcmv1.Pool, 0, len(s.pools))
	for _, p := range s.pools {
		result = append(result, p.DeepCopy())
	}
	return result
}

// GetPool returns a single pool by name, or nil if not found.
func (s *Store) GetPool(name string) *vcmv1.Pool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.pools[name]; ok {
		return p.DeepCopy()
	}
	return nil
}

// --- Lease operations ---

func (s *Store) SetLease(lease *vcmv1.Lease) {
	s.mu.Lock()
	s.leases[lease.Name] = lease.DeepCopy()
	s.mu.Unlock()
	s.notify(EventLeases, EventOverview)
}

func (s *Store) DeleteLease(name string) {
	s.mu.Lock()
	delete(s.leases, name)
	s.mu.Unlock()
	s.notify(EventLeases, EventOverview)
}

func (s *Store) GetLeases() []*vcmv1.Lease {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*vcmv1.Lease, 0, len(s.leases))
	for _, l := range s.leases {
		result = append(result, l.DeepCopy())
	}
	return result
}

// GetLease returns a single lease by name, or nil if not found.
func (s *Store) GetLease(name string) *vcmv1.Lease {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if l, ok := s.leases[name]; ok {
		return l.DeepCopy()
	}
	return nil
}

// GetLeaseByNamespace returns the lease whose lease-namespace label matches
// the given CI namespace (e.g., "ci-op-xxxxx-yyyyy"). Returns nil if not found.
func (s *Store) GetLeaseByNamespace(ns string) *vcmv1.Lease {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, l := range s.leases {
		if l.Labels != nil {
			if val, ok := l.Labels[vcmv1.LeaseNamespace]; ok && val == ns {
				return l.DeepCopy()
			}
		}
	}
	return nil
}

// GetLeasesByJobName returns all leases whose "prow-job-name" annotation
// matches the given full Prow job name.
func (s *Store) GetLeasesByJobName(jobName string) []*vcmv1.Lease {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*vcmv1.Lease
	for _, l := range s.leases {
		if l.Annotations != nil {
			if val, ok := l.Annotations["prow-job-name"]; ok && val == jobName {
				result = append(result, l.DeepCopy())
			}
		}
	}
	return result
}

// --- Network operations ---

func (s *Store) SetNetwork(network *vcmv1.Network) {
	s.mu.Lock()
	s.networks[network.Name] = network.DeepCopy()
	s.mu.Unlock()
	s.notify(EventNetworks, EventOverview)
}

func (s *Store) DeleteNetwork(name string) {
	s.mu.Lock()
	delete(s.networks, name)
	s.mu.Unlock()
	s.notify(EventNetworks, EventOverview)
}

func (s *Store) GetNetworks() []*vcmv1.Network {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*vcmv1.Network, 0, len(s.networks))
	for _, n := range s.networks {
		result = append(result, n.DeepCopy())
	}
	return result
}

// --- Prow operations ---

func (s *Store) SetProwJobs(jobs []prow.JobSummary) {
	s.mu.Lock()
	s.prowJobs = jobs
	s.prowUpdated = time.Now()
	s.mu.Unlock()
	s.notify(EventProw, EventOverview)
}

func (s *Store) GetProwJobs() ([]prow.JobSummary, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]prow.JobSummary, len(s.prowJobs))
	copy(result, s.prowJobs)
	return result, s.prowUpdated
}

// GetProwJobSummary returns the summary for a given Prow job name, or nil if not found.
func (s *Store) GetProwJobSummary(jobName string) *prow.JobSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.prowJobs {
		if s.prowJobs[i].Job == jobName {
			cp := s.prowJobs[i]
			runs := make([]prow.JobRun, len(cp.Runs))
			copy(runs, cp.Runs)
			cp.Runs = runs
			return &cp
		}
	}
	return nil
}

// --- vCenter operations ---

func (s *Store) SetVCenterData(server string, data *vcenter.VCenterData) {
	s.mu.Lock()
	s.vcenterData[server] = data
	s.vcenterUpdated = time.Now()
	s.mu.Unlock()
	s.notify(EventVCenter, EventOverview)
}

func (s *Store) GetVCenterData() (map[string]*vcenter.VCenterData, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]*vcenter.VCenterData, len(s.vcenterData))
	for k, v := range s.vcenterData {
		result[k] = v
	}
	return result, s.vcenterUpdated
}

// GetVMsByClusterID returns all VMs across all vCenters whose ClusterID
// matches the given value, along with the server they belong to.
func (s *Store) GetVMsByClusterID(clusterID string) []vcenter.VMWithServer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []vcenter.VMWithServer
	for server, data := range s.vcenterData {
		for _, vm := range data.VMs {
			if vm.ClusterID == clusterID {
				result = append(result, vcenter.VMWithServer{
					VMInfo:       vm,
					Server:       server,
					InstanceUUID: data.InstanceUUID,
				})
			}
		}
	}
	return result
}

// GetVMsByNamespace returns all VMs across all vCenters whose Namespace
// matches the given CI job namespace (e.g., "ci-op-rwcynqb5").
func (s *Store) GetVMsByNamespace(ns string) []vcenter.VMWithServer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []vcenter.VMWithServer
	for server, data := range s.vcenterData {
		for _, vm := range data.VMs {
			if vm.Namespace == ns {
				result = append(result, vcenter.VMWithServer{
					VMInfo:       vm,
					Server:       server,
					InstanceUUID: data.InstanceUUID,
				})
			}
		}
	}
	return result
}

// --- Overview aggregation ---

// Overview holds summary counts for the dashboard home page.
type Overview struct {
	TotalPools      int
	ActivePools     int
	ExcludedPools   int
	CordondedPools  int
	TotalLeases     int
	FulfilledLeases int
	PendingLeases   int
	FailedLeases    int
	TotalNetworks   int
	ProwJobCount    int
	ProwFailing     int
	ProwPassing     int
	ProwPending     int
	ProwUpdated     time.Time
	VCenterCount    int
	VCenterUpdated  time.Time
}

func (s *Store) GetOverview() Overview {
	s.mu.RLock()
	defer s.mu.RUnlock()

	o := Overview{
		TotalPools:     len(s.pools),
		TotalLeases:    len(s.leases),
		TotalNetworks:  len(s.networks),
		ProwJobCount:   len(s.prowJobs),
		ProwUpdated:    s.prowUpdated,
		VCenterCount:   len(s.vcenterData),
		VCenterUpdated: s.vcenterUpdated,
	}

	for _, p := range s.pools {
		if p.Spec.Exclude {
			o.ExcludedPools++
		} else if p.Spec.NoSchedule {
			o.CordondedPools++
		} else {
			o.ActivePools++
		}
	}

	for _, l := range s.leases {
		switch l.Status.Phase {
		case vcmv1.PHASE_FULFILLED:
			o.FulfilledLeases++
		case vcmv1.PHASE_PENDING:
			o.PendingLeases++
		case vcmv1.PHASE_FAILED:
			o.FailedLeases++
		}
	}

	for _, j := range s.prowJobs {
		switch j.LatestState {
		case "failure":
			o.ProwFailing++
		case "success":
			o.ProwPassing++
		case "pending":
			o.ProwPending++
		}
	}

	return o
}
