package controller

import (
	"context"
	"testing"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "github.com/sorend/csi-driver-syncthing/api/v1alpha1"
)

func TestSyncthingNodeIdentityChange(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	node := &storagev1alpha1.SyncthingNode{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-a"},
		Spec:       storagev1alpha1.SyncthingNodeSpec{NodeName: "worker-a", DeviceID: "new-device"},
		Status:     storagev1alpha1.SyncthingNodeStatus{Ready: true, ObservedDeviceID: "old-device"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&storagev1alpha1.SyncthingNode{}).WithObjects(node).Build()
	r := &SyncthingNodeReconciler{Client: c}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "worker-a"}}); err != nil {
		t.Fatal(err)
	}
	got := &storagev1alpha1.SyncthingNode{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "worker-a"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Ready {
		t.Fatal("node with changed identity must not remain ready")
	}
	condition := apiMeta.FindStatusCondition(got.Status.Conditions, "IdentityChanged")
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != "DeviceIDChanged" {
		t.Fatalf("unexpected IdentityChanged condition: %#v", condition)
	}
}

func TestSyncthingNodeFirstIdentityIsRecorded(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	node := &storagev1alpha1.SyncthingNode{ObjectMeta: metav1.ObjectMeta{Name: "worker-a"}, Spec: storagev1alpha1.SyncthingNodeSpec{NodeName: "worker-a", DeviceID: "device"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&storagev1alpha1.SyncthingNode{}).WithObjects(node).Build()
	r := &SyncthingNodeReconciler{Client: c}
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "worker-a"}}); err != nil {
		t.Fatal(err)
	}
	got := &storagev1alpha1.SyncthingNode{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "worker-a"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.ObservedDeviceID != "device" {
		t.Fatalf("observed device ID = %q, want device", got.Status.ObservedDeviceID)
	}
}
