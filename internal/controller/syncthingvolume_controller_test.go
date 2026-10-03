package controller

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "github.com/sorend/csi-driver-syncthing/api/v1alpha1"
)

func TestSyncthingVolumeReconcile(t *testing.T) {
	tests := []struct {
		name          string
		volume        *storagev1alpha1.SyncthingVolume
		wantPhase     string
		wantFinalizer bool
	}{
		{
			name:      "adds finalizer to newly provisioned volume",
			volume:    &storagev1alpha1.SyncthingVolume{ObjectMeta: metav1.ObjectMeta{Name: "vol"}, Spec: storagev1alpha1.SyncthingVolumeSpec{VolumeHandle: "vol", FolderID: "vol"}},
			wantPhase: "Available", wantFinalizer: true,
		},
		{
			name:      "reports attaching until desired replica is ready",
			volume:    &storagev1alpha1.SyncthingVolume{ObjectMeta: metav1.ObjectMeta{Name: "vol"}, Spec: storagev1alpha1.SyncthingVolumeSpec{DesiredReplicas: []storagev1alpha1.DesiredReplica{{NodeName: "worker-a"}}}},
			wantPhase: "Attaching", wantFinalizer: true,
		},
		{
			name:      "reports ready when all desired replicas are ready",
			volume:    &storagev1alpha1.SyncthingVolume{ObjectMeta: metav1.ObjectMeta{Name: "vol"}, Spec: storagev1alpha1.SyncthingVolumeSpec{DesiredReplicas: []storagev1alpha1.DesiredReplica{{NodeName: "worker-a"}}}, Status: storagev1alpha1.SyncthingVolumeStatus{Replicas: []storagev1alpha1.ReplicaStatus{{NodeName: "worker-a", State: "Ready"}}}},
			wantPhase: "Ready", wantFinalizer: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := storagev1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&storagev1alpha1.SyncthingVolume{}).WithObjects(tt.volume).Build()
			r := &SyncthingVolumeReconciler{Client: c, Scheme: scheme}
			request := reconcile.Request{NamespacedName: types.NamespacedName{Name: "vol"}}
			if _, err := r.Reconcile(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			got := &storagev1alpha1.SyncthingVolume{}
			if err := c.Get(context.Background(), types.NamespacedName{Name: "vol"}, got); err != nil {
				t.Fatal(err)
			}
			if got.Status.Phase != tt.wantPhase {
				t.Errorf("phase = %q, want %q", got.Status.Phase, tt.wantPhase)
			}
			if got.Status.Phase == "" {
				t.Errorf("phase was not initialized")
			}
			if hasFinalizer := contains(got.Finalizers, storagev1alpha1.VolumeProtectionFinalizer); hasFinalizer != tt.wantFinalizer {
				t.Errorf("finalizer = %v, want %v", hasFinalizer, tt.wantFinalizer)
			}
		})
	}
}

func TestDesiredPhaseRequiresEveryReplicaReady(t *testing.T) {
	volume := &storagev1alpha1.SyncthingVolume{
		Spec:   storagev1alpha1.SyncthingVolumeSpec{DesiredReplicas: []storagev1alpha1.DesiredReplica{{NodeName: "a"}, {NodeName: "b"}}},
		Status: storagev1alpha1.SyncthingVolumeStatus{Replicas: []storagev1alpha1.ReplicaStatus{{NodeName: "a", State: "Ready"}, {NodeName: "b", State: "Syncing"}}},
	}
	if got := desiredPhase(volume); got != "Attaching" {
		t.Fatalf("desiredPhase() = %q, want Attaching", got)
	}
}

func TestVolumeDeletionWaitsForReplicaRemoval(t *testing.T) {
	deletionTime := metav1.Now()
	volume := &storagev1alpha1.SyncthingVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "vol", Finalizers: []string{storagev1alpha1.VolumeProtectionFinalizer},
			DeletionTimestamp: &deletionTime,
		},
		Spec: storagev1alpha1.SyncthingVolumeSpec{
			DesiredReplicas: []storagev1alpha1.DesiredReplica{{NodeName: "worker-a"}},
		},
		Status: storagev1alpha1.SyncthingVolumeStatus{
			Replicas: []storagev1alpha1.ReplicaStatus{{NodeName: "worker-a", State: "Ready"}},
		},
	}
	scheme := runtime.NewScheme()
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&storagev1alpha1.SyncthingVolume{}).WithObjects(volume).Build()
	r := &SyncthingVolumeReconciler{Client: c, Scheme: scheme}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: "vol"}}
	for i := 0; i < 4; i++ {
		if _, err := r.Reconcile(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	got := &storagev1alpha1.SyncthingVolume{}
	if err := c.Get(context.Background(), request.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.DesiredReplicas) != 0 {
		t.Fatalf("deleting volume desired replicas = %v, want none", got.Spec.DesiredReplicas)
	}
	if !contains(got.Finalizers, storagev1alpha1.VolumeProtectionFinalizer) {
		t.Fatal("volume protection finalizer removed before replica cleanup")
	}
	if got.Status.Phase != "Deleting" {
		t.Fatalf("phase = %q, want Deleting", got.Status.Phase)
	}
}

func TestVolumeDeletionRemovesFinalizerAfterReplicaIsAbsent(t *testing.T) {
	deletionTime := metav1.Now()
	volume := &storagev1alpha1.SyncthingVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "vol", Finalizers: []string{storagev1alpha1.VolumeProtectionFinalizer},
			DeletionTimestamp: &deletionTime,
		},
		Status: storagev1alpha1.SyncthingVolumeStatus{
			Phase:    "Deleting",
			Replicas: []storagev1alpha1.ReplicaStatus{{NodeName: "worker-a", State: "Absent"}},
		},
	}
	scheme := runtime.NewScheme()
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&storagev1alpha1.SyncthingVolume{}).WithObjects(volume).Build()
	r := &SyncthingVolumeReconciler{Client: c, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "vol"}}); err != nil {
		t.Fatal(err)
	}
	got := &storagev1alpha1.SyncthingVolume{}
	err := c.Get(context.Background(), types.NamespacedName{Name: "vol"}, got)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("volume still exists after replica removal and finalizer cleanup, get error = %v", err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
