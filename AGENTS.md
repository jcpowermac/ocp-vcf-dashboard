# AGENTS.md — Coding Agent Guidelines

## Build / Lint / Test Commands

```bash
make build          # go fmt + go vet + compile to bin/ocp-vcf-dashboard
make fmt            # go fmt ./...
make vet            # go vet ./...
make test           # go test ./... -coverprofile cover.out
make image          # podman build (not docker)
make push           # podman push to quay.io/jcallen/ocp-vcf-dashboard
make deploy         # oc apply -k config/default
make undeploy       # oc delete -k config/default
make run            # build + run locally with default flags
make clean          # rm -rf bin/
```

Run a single test:
```bash
go test ./pkg/prow/ -run TestExtractVersion -v
go test ./pkg/store/ -run TestSetPool -v -count=1
```

Dependencies are vendored but `vendor/` is gitignored. After changing `go.mod`:
```bash
go mod tidy && go mod vendor
```

## Project Layout

```
cmd/dashboard/main.go         Entry point, flag parsing, component wiring
pkg/config/config.go          Reads vcenter-ctrl ConfigMap + Secrets
pkg/k8s/watcher.go            Dynamic informers for Pool/Lease/Network CRDs
pkg/prow/fetcher.go           HTTP fetch from Prow API with caching
pkg/prow/analyzer.go          Filter/aggregate vSphere periodic jobs
pkg/server/server.go          HTTP server, routing, template rendering
pkg/server/sse.go             Server-Sent Events for real-time push
pkg/server/views/*.html       Go html/template files (embedded via //go:embed)
pkg/store/store.go            Thread-safe in-memory store with change notification
pkg/vcenter/collector.go      Periodic govmomi data collection
pkg/vsphere/session.go        vCenter session management (CAPV session wrapper)
static/                       HTMX, SSE extension, CSS (served at /static/)
config/base/                  Kustomize deployment manifests
```

## Go Version & Key Dependencies

- Go 1.23, toolchain 1.24.7
- `k8s.io/{api,apimachinery,client-go}` v0.30.3
- `sigs.k8s.io/controller-runtime` v0.18.5
- `github.com/vmware/govmomi` v0.52.0
- `sigs.k8s.io/cluster-api-provider-vsphere` v1.11.1 (session package)
- `github.com/openshift-splat-team/vsphere-capacity-manager` (CRD types)
- `k8s.io/klog/v2` for all logging

Three `replace` directives exist in go.mod — do not remove them.

## Import Ordering

Three groups separated by blank lines: stdlib, third-party, local.

```go
import (
    "context"
    "fmt"
    "time"

    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/klog/v2"
    vcmv1 "github.com/openshift-splat-team/vsphere-capacity-manager/pkg/apis/vspherecapacitymanager.splat.io/v1"

    "github.com/jcpowermac/ocp-vcf-dashboard/pkg/store"
)
```

Standard import aliases:
- `corev1` → `k8s.io/api/core/v1`
- `metav1` → `k8s.io/apimachinery/pkg/apis/meta/v1`
- `vcmv1` → `github.com/openshift-splat-team/vsphere-capacity-manager/pkg/apis/vspherecapacitymanager.splat.io/v1`
- `ctrlclient` → `sigs.k8s.io/controller-runtime/pkg/client`
- `k8swatcher` → `github.com/jcpowermac/ocp-vcf-dashboard/pkg/k8s`

## Naming Conventions

- **Packages**: single lowercase word (`store`, `prow`, `vcenter`, `config`)
- **Receivers**: single letter matching type initial, always pointer (`func (s *Store)`, `func (m *Metadata)`)
- **Constructors**: `New()` or `NewXxx()` (e.g., `NewWatcher()`, `NewMetadataFromCredentials()`)
- **Getters/Setters**: `GetPools()` / `SetPool()` / `DeletePool()`
- **Lock-held helpers**: `xxxLocked` suffix (`addCredentialsLocked`, `getCredentialsLocked`)
- **HTTP handlers**: `handleXxx` (e.g., `handleOverview`, `handlePoolsFragment`)
- **Constants**: exported `PascalCase` (`MinOCPVersion`, `DefaultPollInterval`), unexported `camelCase` (`httpTimeout`)
- **CRD phase constants**: use `vcmv1.PHASE_FULFILLED`, `vcmv1.PHASE_PENDING`, `vcmv1.PHASE_FAILED`

## Error Handling

Always wrap with `fmt.Errorf` and `%w`. Never use `errors.New`. Use lowercase gerund prefix:

```go
return nil, fmt.Errorf("creating container view: %w", err)
return nil, fmt.Errorf("retrieving clusters: %w", err)
return nil, fmt.Errorf("failed to read username from secret %s/%s: %w", ns, name, err)
```

In `main()` only, use `klog.Fatalf` for unrecoverable startup errors.
For non-fatal runtime errors, use `klog.Errorf`.

## Logging

Use `klog` exclusively. No other logging libraries.

```go
klog.Infof("Starting dashboard on %s", addr)       // startup lifecycle
klog.Errorf("vCenter %s session error: %v", s, err) // non-fatal errors
klog.Fatalf("Failed to create client: %v", err)     // fatal, main() only
klog.V(2).Infof("Prow refresh: %d jobs", len(j))    // verbose/periodic status
```

- Messages start with a capital letter
- Use `%v` (not `%w`) when logging errors
- `V(2)` is the only verbosity level used (periodic/routine)

## Concurrency Patterns

- `sync.RWMutex` for the Store (read-heavy; `RLock` for getters, `Lock` for setters)
- Explicit `Unlock()` before calling `notify()` — do not use `defer` for setters
- Per-server `sync.Mutex` in Metadata to avoid head-of-line blocking across vCenters
- Always `DeepCopy()` CRD objects when storing or returning from the Store

## Template / HTML Patterns

- Templates are embedded via `//go:embed views/*.html` in `pkg/server/server.go`
- Each page defines a named block: `{{define "pools-content"}}...{{end}}`
- HTMX SSE integration: `hx-ext="sse"`, `sse-connect="/api/sse/{resource}"`
- CSS classes use kebab-case: `data-table`, `progress-bar`, `status-fulfilled`
- Template names use kebab-case: `overview-content`, `pools-content`
- Custom template functions are registered in the `funcMap` in `server.go`

## Struct Tags

JSON tags use `snake_case`. Use `omitempty` only for optional fields:

```go
type VMInfo struct {
    Name       string `json:"name"`
    PowerState string `json:"power_state"`
    NumCPUs    int32  `json:"num_cpus"`
}
```

Config structs matching YAML keys use `camelCase` tags: `json:"secretRef"`.
Internal-only structs (e.g., `VCenterCredential`) have no tags.

## Container Build

- Multi-stage: `golang:1.23` builder → `gcr.io/distroless/static:nonroot`
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`
- Uses **podman**, not docker
- Runs as non-root user `65532:65532`

## Deployment

- Namespace: `vsphere-infra-helpers`
- Reuses the existing `vsphere-cleanup-config` ConfigMap and `vcenter-*-credentials` Secrets
- OAuth proxy sidecar for OpenShift authentication
- Kustomize manifests in `config/base/` with overlay in `config/default/`
