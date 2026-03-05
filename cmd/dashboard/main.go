package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/config"
	k8swatcher "github.com/jcpowermac/ocp-vcf-dashboard/pkg/k8s"
	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/prow"
	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/server"
	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/store"
	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/vcenter"
	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/vsphere"
)

func main() {
	var (
		listenAddr          string
		secretNamespace     string
		configName          string
		consoleURL          string
		prowPollInterval    time.Duration
		vcenterPollInterval time.Duration
	)

	flag.StringVar(&listenAddr, "listen-address", ":8080", "Address to listen on for the web UI")
	flag.StringVar(&secretNamespace, "secret-namespace", "vsphere-infra-helpers", "Namespace for vCenter Secrets and ConfigMap")
	flag.StringVar(&configName, "config-name", "vsphere-cleanup-config", "Name of the vCenter configuration ConfigMap")
	flag.StringVar(&consoleURL, "console-url", "https://console-openshift-console.apps.build02.vmc.ci.openshift.org", "OpenShift console base URL for namespace links")
	flag.DurationVar(&prowPollInterval, "prow-poll-interval", 5*time.Minute, "How often to refresh Prow job data")
	flag.DurationVar(&vcenterPollInterval, "vcenter-poll-interval", 5*time.Minute, "How often to poll vCenter data (0 to disable)")

	klog.InitFlags(nil)
	flag.Parse()

	// Route controller-runtime logs through klog so the CAPV session
	// package (and any other controller-runtime user) does not emit
	// the "log.SetLogger(...) was never called" warning.
	ctrllog.SetLogger(klog.NewKlogr())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		klog.Info("Received shutdown signal")
		cancel()
	}()

	// Build Kubernetes config
	restConfig, err := buildKubeConfig()
	if err != nil {
		klog.Fatalf("Failed to create kubernetes config: %v", err)
	}

	// Create dynamic client for CRD watching
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		klog.Fatalf("Failed to create dynamic client: %v", err)
	}

	// Create controller-runtime client for reading ConfigMaps/Secrets
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	crClient, err := ctrlclient.New(restConfig, ctrlclient.Options{Scheme: scheme})
	if err != nil {
		klog.Fatalf("Failed to create controller-runtime client: %v", err)
	}

	// Read vCenter configuration from the shared ConfigMap
	cfg, err := config.ReadConfig(secretNamespace, configName, crClient)
	if err != nil {
		klog.Fatalf("Failed to read config: %v", err)
	}
	klog.Infof("Loaded configuration with %d vCenters", len(cfg.VCenters))

	// Resolve vCenter credentials from Secrets
	creds, err := config.ResolveCredentials(secretNamespace, cfg.VCenters, crClient)
	if err != nil {
		klog.Fatalf("Failed to resolve vCenter credentials: %v", err)
	}
	klog.Infof("Resolved credentials for %d vCenters", len(creds))

	// Build vSphere session metadata
	metadata, err := vsphere.NewMetadataFromCredentials(creds)
	if err != nil {
		klog.Fatalf("Failed to create vSphere metadata: %v", err)
	}

	// Initialize in-memory store
	dataStore := store.New()

	// Start CRD watcher
	watcher := k8swatcher.NewWatcher(dynamicClient, secretNamespace, dataStore)
	go func() {
		if err := watcher.Run(ctx); err != nil {
			klog.Errorf("CRD watcher error: %v", err)
		}
	}()

	// Start Prow fetcher
	prowFetcher := prow.NewFetcher(prowPollInterval)
	go prowFetcher.Run(ctx, func(jobs []prow.JobSummary) {
		dataStore.SetProwJobs(jobs)
	})

	// Start vCenter collector (if enabled)
	if vcenterPollInterval > 0 {
		collector := vcenter.NewCollector(metadata, vcenterPollInterval)
		go collector.Run(ctx, func(srv string, data *vcenter.VCenterData) {
			dataStore.SetVCenterData(srv, data)
		})
		klog.Infof("vCenter collection enabled with %v interval", vcenterPollInterval)
	} else {
		klog.Info("vCenter collection disabled")
	}

	// Create and start HTTP server
	srv, err := server.New(dataStore, consoleURL)
	if err != nil {
		klog.Fatalf("Failed to create HTTP server: %v", err)
	}

	httpServer := &http.Server{
		Addr:         listenAddr,
		Handler:      srv.Handler(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // SSE requires no write timeout
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		klog.Infof("Starting dashboard on %s", listenAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-ctx.Done()

	klog.Info("Shutting down...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		klog.Errorf("HTTP server shutdown error: %v", err)
	}
}

func buildKubeConfig() (*rest.Config, error) {
	// Try in-cluster first
	cfg, err := rest.InClusterConfig()
	if err == nil {
		return cfg, nil
	}

	// Fallback to default kubeconfig (respects --kubeconfig flag registered by client-go)
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides).ClientConfig()
}
