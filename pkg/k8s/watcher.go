// Package k8s provides Kubernetes CRD watch functionality using
// client-go informers for Pools, Leases, and Networks.
package k8s

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	vcmv1 "github.com/openshift-splat-team/vsphere-capacity-manager/pkg/apis/vspherecapacitymanager.splat.io/v1"

	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/store"
)

var (
	poolGVR = schema.GroupVersionResource{
		Group:    "vspherecapacitymanager.splat.io",
		Version:  "v1",
		Resource: "pools",
	}
	leaseGVR = schema.GroupVersionResource{
		Group:    "vspherecapacitymanager.splat.io",
		Version:  "v1",
		Resource: "leases",
	}
	networkGVR = schema.GroupVersionResource{
		Group:    "vspherecapacitymanager.splat.io",
		Version:  "v1",
		Resource: "networks",
	}
)

// Watcher uses dynamic informers to watch CRDs and update the store.
type Watcher struct {
	dynamicClient dynamic.Interface
	namespace     string
	store         *store.Store
}

// NewWatcher creates a new CRD watcher.
func NewWatcher(dynamicClient dynamic.Interface, namespace string, s *store.Store) *Watcher {
	return &Watcher{
		dynamicClient: dynamicClient,
		namespace:     namespace,
		store:         s,
	}
}

// Run starts watching all three CRD types. It blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		w.dynamicClient,
		30*time.Second,
		w.namespace,
		nil,
	)

	// Pool informer
	poolInformer := factory.ForResource(poolGVR)
	poolInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			w.handlePool(obj)
		},
		UpdateFunc: func(_, obj interface{}) {
			w.handlePool(obj)
		},
		DeleteFunc: func(obj interface{}) {
			if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = d.Obj
			}
			if u, ok := obj.(*unstructured.Unstructured); ok {
				w.store.DeletePool(u.GetName())
			}
		},
	})

	// Lease informer
	leaseInformer := factory.ForResource(leaseGVR)
	leaseInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			w.handleLease(obj)
		},
		UpdateFunc: func(_, obj interface{}) {
			w.handleLease(obj)
		},
		DeleteFunc: func(obj interface{}) {
			if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = d.Obj
			}
			if u, ok := obj.(*unstructured.Unstructured); ok {
				w.store.DeleteLease(u.GetName())
			}
		},
	})

	// Network informer
	networkInformer := factory.ForResource(networkGVR)
	networkInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			w.handleNetwork(obj)
		},
		UpdateFunc: func(_, obj interface{}) {
			w.handleNetwork(obj)
		},
		DeleteFunc: func(obj interface{}) {
			if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = d.Obj
			}
			if u, ok := obj.(*unstructured.Unstructured); ok {
				w.store.DeleteNetwork(u.GetName())
			}
		},
	})

	klog.Infof("Starting CRD watchers for namespace %s", w.namespace)
	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())
	klog.Infof("CRD informer caches synced")

	<-ctx.Done()
	return nil
}

func (w *Watcher) handlePool(obj interface{}) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}

	var pool vcmv1.Pool
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &pool); err != nil {
		klog.Errorf("Failed to convert pool %s: %v", u.GetName(), err)
		return
	}

	w.store.SetPool(&pool)
}

func (w *Watcher) handleLease(obj interface{}) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}

	var lease vcmv1.Lease
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &lease); err != nil {
		klog.Errorf("Failed to convert lease %s: %v", u.GetName(), err)
		return
	}

	w.store.SetLease(&lease)
}

func (w *Watcher) handleNetwork(obj interface{}) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}

	var network vcmv1.Network
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &network); err != nil {
		klog.Errorf("Failed to convert network %s: %v", u.GetName(), err)
		return
	}

	w.store.SetNetwork(&network)
}

// Ensure interfaces are satisfied at compile time
var _ watch.Interface = nil
var _ = metav1.ListOptions{}
var _ = fmt.Sprintf
