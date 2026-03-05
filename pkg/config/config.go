// Package config reads the vcenter-ctrl ConfigMap and Secrets to obtain
// vCenter connection parameters. It reuses the exact same ConfigMap
// (vsphere-cleanup-config) and Secret format (username/password keys)
// deployed for the vsphere-capacity-manager-vcenter-ctrl controller.
package config

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// Secret key names following the CAPV identity pattern.
const (
	UsernameKey = "username"
	PasswordKey = "password"
)

// VCenterConfig maps a vCenter server to its credential Secret.
type VCenterConfig struct {
	Server    string `json:"server"`
	SecretRef string `json:"secretRef"`
}

// DashboardConfig holds the parsed configuration from the shared ConfigMap.
// We only care about the vcenters list; cleanup/safety/features/protection
// fields are ignored by the dashboard.
type DashboardConfig struct {
	VCenters []VCenterConfig `json:"vcenters"`
}

// VCenterCredential holds resolved credentials for a single vCenter.
type VCenterCredential struct {
	Server   string
	Username string
	Password string
}

// ReadConfig reads and parses the controller ConfigMap, extracting only
// the vcenters list needed by the dashboard.
func ReadConfig(namespace, configMapName string, c client.Client) (*DashboardConfig, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Name: configMapName, Namespace: namespace}, &cm); err != nil {
		return nil, fmt.Errorf("failed to get ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	configData, ok := cm.Data["config.yaml"]
	if !ok {
		return nil, fmt.Errorf("ConfigMap %s/%s missing 'config.yaml' key", namespace, configMapName)
	}

	var config DashboardConfig
	if err := yaml.Unmarshal([]byte(configData), &config); err != nil {
		return nil, fmt.Errorf("failed to parse config.yaml from ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	if len(config.VCenters) == 0 {
		return nil, fmt.Errorf("no vcenters configured in ConfigMap %s/%s", namespace, configMapName)
	}

	return &config, nil
}

// GetSecret reads a specific data key from a Kubernetes Secret.
func GetSecret(namespace, secret, dataKey string, c client.Client) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var s corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Name: secret, Namespace: namespace}, &s); err != nil {
		return "", err
	}

	if encodedValue, ok := s.Data[dataKey]; ok {
		return string(encodedValue), nil
	}
	return "", fmt.Errorf("secret %s/%s/%s not found", namespace, secret, dataKey)
}

// ResolveCredentials reads all vCenter credentials from the referenced Secrets.
func ResolveCredentials(namespace string, vcenters []VCenterConfig, c client.Client) ([]VCenterCredential, error) {
	var creds []VCenterCredential

	for _, vc := range vcenters {
		username, err := GetSecret(namespace, vc.SecretRef, UsernameKey, c)
		if err != nil {
			return nil, fmt.Errorf("failed to read username from secret %s/%s: %w", namespace, vc.SecretRef, err)
		}

		password, err := GetSecret(namespace, vc.SecretRef, PasswordKey, c)
		if err != nil {
			return nil, fmt.Errorf("failed to read password from secret %s/%s: %w", namespace, vc.SecretRef, err)
		}

		creds = append(creds, VCenterCredential{
			Server:   vc.Server,
			Username: username,
			Password: password,
		})
	}

	return creds, nil
}
