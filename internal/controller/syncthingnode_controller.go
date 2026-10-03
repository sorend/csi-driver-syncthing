package controller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "github.com/sorend/csi-driver-syncthing/api/v1alpha1"
)

// SyncthingNodeReconciler detects replacement of a Syncthing identity under an
// existing Kubernetes node name without silently accepting the new identity.
type SyncthingNodeReconciler struct {
	client.Client
}

func (r *SyncthingNodeReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	node := &storagev1alpha1.SyncthingNode{}
	if err := r.Get(ctx, req.NamespacedName, node); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	changed := node.Status.ObservedDeviceID != "" && node.Spec.DeviceID != node.Status.ObservedDeviceID
	statusChanged := false
	if changed {
		before := node.DeepCopy()
		node.Status.Ready = false
		apiMeta.SetStatusCondition(&node.Status.Conditions, metav1.Condition{
			Type: "IdentityChanged", Status: metav1.ConditionTrue,
			Reason: "DeviceIDChanged", Message: "Syncthing identity changed for this Kubernetes node",
		})
		statusChanged = before.Status.Ready != node.Status.Ready || !conditionsEqual(before.Status.Conditions, node.Status.Conditions)
	} else {
		if node.Status.ObservedDeviceID == "" && node.Spec.DeviceID != "" {
			node.Status.ObservedDeviceID = node.Spec.DeviceID
			statusChanged = true
		}
		if apiMeta.SetStatusCondition(&node.Status.Conditions, metav1.Condition{
			Type: "IdentityChanged", Status: metav1.ConditionFalse,
			Reason: "IdentityStable", Message: "Syncthing identity matches the previously observed identity",
		}) {
			statusChanged = true
		}
	}
	if statusChanged {
		if err := r.Status().Update(ctx, node); err != nil && !apierrors.IsConflict(err) {
			return reconcile.Result{}, err
		} else if err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

func conditionsEqual(a, b []metav1.Condition) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (r *SyncthingNodeReconciler) SetupWithManager(mgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&storagev1alpha1.SyncthingNode{}).Complete(r)
}
