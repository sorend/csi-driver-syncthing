package controller

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "github.com/sorend/csi-driver-syncthing/api/v1alpha1"
)

// SyncthingVolumeReconciler maintains the volume protection finalizer and reports
// the lifecycle implied by desired replica membership and agent-published status.
type SyncthingVolumeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *SyncthingVolumeReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx)
	volume := &storagev1alpha1.SyncthingVolume{}
	if err := r.Get(ctx, req.NamespacedName, volume); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	if !volume.DeletionTimestamp.IsZero() {
		if volume.Status.Phase != "Deleting" {
			volume.Status.Phase = "Deleting"
			if err := r.Status().Update(ctx, volume); err != nil {
				return reconcile.Result{}, err
			}
			return reconcile.Result{}, nil
		}
		if len(volume.Spec.DesiredReplicas) != 0 {
			volume.Spec.DesiredReplicas = nil
			if err := r.Update(ctx, volume); err != nil {
				return reconcile.Result{}, err
			}
			return reconcile.Result{}, nil
		}
		if !replicasRemoved(volume.Status.Replicas) {
			return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
		}
		if !controllerutil.ContainsFinalizer(volume, storagev1alpha1.VolumeProtectionFinalizer) {
			return reconcile.Result{}, nil
		}
		controllerutil.RemoveFinalizer(volume, storagev1alpha1.VolumeProtectionFinalizer)
		if err := r.Update(ctx, volume); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(volume, storagev1alpha1.VolumeProtectionFinalizer) {
		controllerutil.AddFinalizer(volume, storagev1alpha1.VolumeProtectionFinalizer)
		if err := r.Update(ctx, volume); err != nil {
			return reconcile.Result{}, err
		}
	}

	phase := desiredPhase(volume)
	if volume.Status.Phase != phase || volume.Status.ObservedGeneration != volume.Generation {
		volume.Status.Phase = phase
		volume.Status.ObservedGeneration = volume.Generation
		if err := r.Status().Update(ctx, volume); err != nil {
			if apierrors.IsConflict(err) {
				logger.V(1).Info("volume changed during status update")
			}
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

func desiredPhase(volume *storagev1alpha1.SyncthingVolume) string {
	if len(volume.Spec.DesiredReplicas) == 0 {
		return "Available"
	}
	states := make(map[string]string, len(volume.Status.Replicas))
	for _, replica := range volume.Status.Replicas {
		states[replica.NodeName] = replica.State
	}
	for _, desired := range volume.Spec.DesiredReplicas {
		if desired.Mode == "cache" {
			continue
		}
		if states[desired.NodeName] != "Ready" && states[desired.NodeName] != "Mounted" {
			return "Attaching"
		}
	}
	return "Ready"
}

func replicasRemoved(replicas []storagev1alpha1.ReplicaStatus) bool {
	for _, replica := range replicas {
		if replica.State != "Absent" {
			return false
		}
	}
	return true
}

func (r *SyncthingVolumeReconciler) SetupWithManager(mgr manager.Manager) error {
	r.Scheme = mgr.GetScheme()
	return ctrl.NewControllerManagedBy(mgr).For(&storagev1alpha1.SyncthingVolume{}).Complete(r)
}
