// Package vsphere provides vCenter session management, reusing the
// CAPV session package for govmomi client lifecycle management.
// This mirrors the pattern from vsphere-capacity-manager-vcenter-ctrl.
package vsphere

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/vmware/govmomi/view"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/session"

	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/config"
)

const sessionTimeout = 120 * time.Second

// Metadata holds vCenter session state for all configured servers.
type Metadata struct {
	mu       sync.Mutex
	serverMu map[string]*sync.Mutex

	sessions    map[string]*session.Session
	credentials map[string]*session.Params
	Credentials map[string]config.VCenterCredential
}

// NewMetadata initializes a new Metadata object.
func NewMetadata() *Metadata {
	return &Metadata{
		serverMu:    make(map[string]*sync.Mutex),
		sessions:    make(map[string]*session.Session),
		credentials: make(map[string]*session.Params),
		Credentials: make(map[string]config.VCenterCredential),
	}
}

// NewMetadataFromCredentials creates Metadata pre-populated with credentials.
func NewMetadataFromCredentials(creds []config.VCenterCredential) (*Metadata, error) {
	m := NewMetadata()
	for _, c := range creds {
		if _, err := m.AddCredentials(c.Server, c.Username, c.Password); err != nil {
			return nil, fmt.Errorf("failed to add credentials for %s: %w", c.Server, err)
		}
	}
	return m, nil
}

// AddCredentials stores session params for a vCenter server.
func (m *Metadata) AddCredentials(server, username, password string) (*session.Params, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.addCredentialsLocked(server, username, password)
}

func (m *Metadata) addCredentialsLocked(server, username, password string) (*session.Params, error) {
	if _, ok := m.Credentials[server]; !ok {
		m.Credentials[server] = config.VCenterCredential{
			Server:   server,
			Username: username,
			Password: password,
		}
	}

	if m.credentials == nil {
		m.credentials = make(map[string]*session.Params)
	}

	if _, ok := m.credentials[server]; !ok {
		m.credentials[server] = session.NewParams().WithServer(server).WithUserInfo(username, password)
	}

	return m.credentials[server], nil
}

func (m *Metadata) getCredentialsLocked(server string) (*session.Params, error) {
	if _, ok := m.credentials[server]; !ok {
		if c, ok := m.Credentials[server]; ok {
			_, err := m.addCredentialsLocked(server, c.Username, c.Password)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("credentials for %s not found", server)
		}
	}
	return m.credentials[server], nil
}

func (m *Metadata) serverMuLocked(server string) *sync.Mutex {
	if m.serverMu == nil {
		m.serverMu = make(map[string]*sync.Mutex)
	}
	mu, ok := m.serverMu[server]
	if !ok {
		mu = &sync.Mutex{}
		m.serverMu[server] = mu
	}
	return mu
}

// Session returns a govmomi session for the given vCenter server.
// Uses per-server locking so that a slow vCenter does not block others.
func (m *Metadata) Session(ctx context.Context, server string) (*session.Session, error) {
	m.mu.Lock()

	if m.sessions == nil {
		m.sessions = make(map[string]*session.Session)
	}

	params, err := m.getCredentialsLocked(server)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}

	smu := m.serverMuLocked(server)
	m.mu.Unlock()

	smu.Lock()
	defer smu.Unlock()

	timeoutCtx, cancel := context.WithTimeout(ctx, sessionTimeout)
	defer cancel()

	s, err := session.GetOrCreate(timeoutCtx, params)
	if err != nil {
		return nil, fmt.Errorf("session for %s: %w", server, err)
	}

	m.mu.Lock()
	m.sessions[server] = s
	m.mu.Unlock()

	return s, nil
}

// Servers returns the list of configured vCenter server addresses.
func (m *Metadata) Servers() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	servers := make([]string, 0, len(m.Credentials))
	for s := range m.Credentials {
		servers = append(servers, s)
	}
	return servers
}

// ContainerView creates a new container view for the given vCenter server.
func (m *Metadata) ContainerView(ctx context.Context, server string) (*view.ContainerView, error) {
	s, err := m.Session(ctx, server)
	if err != nil {
		return nil, err
	}

	viewMgr := view.NewManager(s.Client.Client)
	return viewMgr.CreateContainerView(ctx, s.Client.ServiceContent.RootFolder, nil, true)
}
