// Package vcenter provides periodic collection of vCenter inventory data
// (hosts, clusters, datastores, VMs) using govmomi, reusing the same
// session management pattern as vsphere-capacity-manager-vcenter-ctrl.
package vcenter

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	"k8s.io/klog/v2"

	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/vsphere"
)

// DefaultPollInterval is the default interval between vCenter data collection cycles.
const DefaultPollInterval = 5 * time.Minute

// VCenterData holds the collected inventory data for a single vCenter.
type VCenterData struct {
	Server       string        `json:"server"`
	InstanceUUID string        `json:"instance_uuid"`
	CollectedAt  time.Time     `json:"collected_at"`
	Clusters     []ClusterInfo `json:"clusters"`
	VMs          []VMInfo      `json:"vms"`
	Error        string        `json:"error,omitempty"`
}

// ClusterInfo holds cluster-level resource and DRS information.
type ClusterInfo struct {
	Name              string `json:"name"`
	NumHosts          int32  `json:"num_hosts"`
	NumEffectiveHosts int32  `json:"num_effective_hosts"`
	TotalCPUCores     int16  `json:"total_cpu_cores"`
	TotalCPUMHz       int32  `json:"total_cpu_mhz"`
	DrsScore          int32  `json:"drs_score"`
	CurrentBalance    int32  `json:"current_balance"`
	TargetBalance     int32  `json:"target_balance"`
	NumVmotions       int32  `json:"num_vmotions"`
	CpuDemandMHz      int32  `json:"cpu_demand_mhz"`
	CpuCapacityMHz    int32  `json:"cpu_capacity_mhz"`
	MemDemandMB       int32  `json:"mem_demand_mb"`
	MemCapacityMB     int32  `json:"mem_capacity_mb"`
}

// VMWithServer pairs a VMInfo with the vCenter server it belongs to.
type VMWithServer struct {
	VMInfo
	Server       string `json:"server"`
	InstanceUUID string `json:"instance_uuid"`
}

// VMInfo holds VM information including CPU and memory quick stats.
type VMInfo struct {
	Name         string `json:"name"`
	MoRef        string `json:"moref"`
	PowerState   string `json:"power_state"`
	NumCPUs      int32  `json:"num_cpus"`
	MemoryMB     int32  `json:"memory_mb"`
	CpuUsageMHz  int32  `json:"cpu_usage_mhz"`
	CpuDemandMHz int32  `json:"cpu_demand_mhz"`
	CpuReadiness int32  `json:"cpu_readiness"`
	ClusterID    string `json:"cluster_id"`
	Namespace    string `json:"namespace"`
}

// Collector periodically collects data from all configured vCenters.
type Collector struct {
	metadata *vsphere.Metadata
	interval time.Duration
}

// NewCollector creates a new vCenter data collector.
func NewCollector(metadata *vsphere.Metadata, interval time.Duration) *Collector {
	return &Collector{
		metadata: metadata,
		interval: interval,
	}
}

// Run starts the periodic collection loop. It blocks until ctx is cancelled.
func (c *Collector) Run(ctx context.Context, callback func(server string, data *VCenterData)) {
	// Initial collection
	c.collectAll(ctx, callback)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collectAll(ctx, callback)
		}
	}
}

func (c *Collector) collectAll(ctx context.Context, callback func(string, *VCenterData)) {
	servers := c.metadata.Servers()
	klog.V(2).Infof("Collecting vCenter data from %d servers", len(servers))

	var wg sync.WaitGroup
	for _, server := range servers {
		wg.Add(1)
		go func(srv string) {
			defer wg.Done()
			data := c.collectServer(ctx, srv)
			callback(srv, data)
		}(server)
	}
	wg.Wait()
}

func (c *Collector) collectServer(ctx context.Context, server string) *VCenterData {
	data := &VCenterData{
		Server:      server,
		CollectedAt: time.Now(),
	}

	sess, err := c.metadata.Session(ctx, server)
	if err != nil {
		data.Error = fmt.Sprintf("session error: %v", err)
		klog.Errorf("vCenter %s session error: %v", server, err)
		return data
	}

	// sess.Client is a *govmomi.Client, sess.Client.Client is the vim25.Client
	vimClient := sess.Client.Client
	data.InstanceUUID = vimClient.ServiceContent.About.InstanceUuid

	// Collect clusters
	clusters, err := collectClusters(ctx, vimClient)
	if err != nil {
		klog.Errorf("vCenter %s cluster collection error: %v", server, err)
	} else {
		data.Clusters = clusters
	}

	// Collect VMs (only CI-related ones to reduce noise)
	vms, err := collectVMs(ctx, vimClient)
	if err != nil {
		klog.Errorf("vCenter %s VM collection error: %v", server, err)
	} else {
		data.VMs = vms
	}

	klog.V(2).Infof("vCenter %s: %d clusters, %d VMs",
		server, len(data.Clusters), len(data.VMs))

	return data
}

func collectClusters(ctx context.Context, c *vim25.Client) ([]ClusterInfo, error) {
	mgr := view.NewManager(c)
	cv, err := mgr.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"ClusterComputeResource"}, true)
	if err != nil {
		return nil, fmt.Errorf("creating cluster container view: %w", err)
	}
	defer cv.Destroy(ctx)

	var clusters []mo.ClusterComputeResource
	err = cv.Retrieve(ctx, []string{"ClusterComputeResource"}, []string{"name", "summary"}, &clusters)
	if err != nil {
		return nil, fmt.Errorf("retrieving clusters: %w", err)
	}

	var result []ClusterInfo
	for _, cl := range clusters {
		info := ClusterInfo{
			Name: cl.Name,
		}

		if s := cl.Summary.GetComputeResourceSummary(); s != nil {
			info.TotalCPUMHz = s.TotalCpu
			info.NumHosts = s.NumHosts
			info.NumEffectiveHosts = s.NumEffectiveHosts
			info.TotalCPUCores = s.NumCpuCores
		}

		// Extract DRS-specific fields from ClusterComputeResourceSummary
		if cs, ok := cl.Summary.(*types.ClusterComputeResourceSummary); ok {
			info.DrsScore = cs.DrsScore
			info.CurrentBalance = cs.CurrentBalance
			info.TargetBalance = cs.TargetBalance
			info.NumVmotions = cs.NumVmotions
			if cs.UsageSummary != nil {
				info.CpuDemandMHz = cs.UsageSummary.CpuDemandMhz
				info.CpuCapacityMHz = cs.UsageSummary.TotalCpuCapacityMhz
				info.MemDemandMB = cs.UsageSummary.MemDemandMB
				info.MemCapacityMB = cs.UsageSummary.TotalMemCapacityMB
			}
		}

		result = append(result, info)
	}

	return result, nil
}

func collectVMs(ctx context.Context, c *vim25.Client) ([]VMInfo, error) {
	mgr := view.NewManager(c)
	cv, err := mgr.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"VirtualMachine"}, true)
	if err != nil {
		return nil, fmt.Errorf("creating VM container view: %w", err)
	}
	defer cv.Destroy(ctx)

	var vms []mo.VirtualMachine
	err = cv.Retrieve(ctx, []string{"VirtualMachine"},
		[]string{"name", "summary", "runtime.powerState", "config.hardware.numCPU", "config.hardware.memoryMB"},
		&vms)
	if err != nil {
		return nil, fmt.Errorf("retrieving VMs: %w", err)
	}

	var result []VMInfo
	for _, vm := range vms {
		// Only include CI-related VMs to keep the list manageable
		if !isCIVM(vm.Name) {
			continue
		}

		info := VMInfo{
			Name:         vm.Name,
			MoRef:        vm.Self.Value,
			PowerState:   string(vm.Runtime.PowerState),
			CpuUsageMHz:  vm.Summary.QuickStats.OverallCpuUsage,
			CpuDemandMHz: vm.Summary.QuickStats.OverallCpuDemand,
			CpuReadiness: vm.Summary.QuickStats.OverallCpuReadiness,
			ClusterID:    extractClusterID(vm.Name),
			Namespace:    extractNamespace(vm.Name),
		}
		if vm.Config != nil {
			info.NumCPUs = vm.Config.Hardware.NumCPU
			info.MemoryMB = vm.Config.Hardware.MemoryMB
		}
		result = append(result, info)
	}

	return result, nil
}

// extractNamespace extracts the CI job namespace from a VM name.
// For ci-op VMs like "ci-op-rwcynqb5-4d914-zqxt2-master-0", the namespace
// is the first 3 segments: "ci-op-rwcynqb5" (matching the lease-namespace label).
// Returns empty string if the name doesn't match the ci-op pattern.
func extractNamespace(name string) string {
	parts := strings.Split(name, "-")
	if len(parts) >= 3 && parts[0] == "ci" && parts[1] == "op" {
		return strings.Join(parts[:3], "-")
	}
	return ""
}

// extractClusterID extracts the infra ID (cluster ID) from a VM name.
// For ci-op VMs like "ci-op-rwcynqb5-4d914-zqxt2-master-0", the infra ID
// is the first 4 segments: "ci-op-rwcynqb5-4d914" (namespace + unique hash).
// Returns empty string if the name doesn't match the ci-op pattern.
func extractClusterID(name string) string {
	parts := strings.Split(name, "-")
	if len(parts) >= 4 && parts[0] == "ci" && parts[1] == "op" {
		return strings.Join(parts[:4], "-")
	}
	return ""
}

// isCIVM returns true if a VM name matches CI job patterns.
func isCIVM(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), "ci-")
}
