package csi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	storagev1alpha1 "github.com/sorend/csi-driver-syncthing/api/v1alpha1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Controller struct {
	csi.UnimplementedControllerServer
	Client client.Client
	Poll   time.Duration
}

func (s *Controller) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume name is required")
	}
	if err := validateCapabilities(req.GetVolumeCapabilities()); err != nil {
		return nil, err
	}
	if req.GetCapacityRange() == nil {
		return nil, status.Error(codes.InvalidArgument, "capacity range is required")
	}
	if req.GetVolumeContentSource() != nil {
		return nil, status.Error(codes.InvalidArgument, "volume cloning is not supported")
	}
	for _, key := range []string{"backupClass", "backupPolicy"} {
		if req.GetParameters()[key] != "" {
			return nil, status.Errorf(codes.InvalidArgument, "StorageClass parameter %q is not supported", key)
		}
	}
	name := safeVolumeName(req.GetName())
	if len(name) > 63 || !dnsLabelPattern.MatchString(name) {
		return nil, status.Error(codes.InvalidArgument, "volume name cannot be represented as a Kubernetes DNS label")
	}
	volume := &storagev1alpha1.SyncthingVolume{}
	err := s.Client.Get(ctx, types.NamespacedName{Name: name}, volume)
	capacity := req.GetCapacityRange().GetRequiredBytes()
	if capacity < 0 {
		return nil, status.Error(codes.InvalidArgument, "required capacity cannot be negative")
	}
	if capacity == 0 && req.GetCapacityRange().GetLimitBytes() == 0 {
		return nil, status.Error(codes.InvalidArgument, "a positive volume capacity is required")
	}
	if req.GetCapacityRange().GetLimitBytes() > 0 && capacity > req.GetCapacityRange().GetLimitBytes() {
		return nil, status.Error(codes.InvalidArgument, "required capacity exceeds capacity limit")
	}
	if capacity == 0 && req.GetCapacityRange().GetLimitBytes() > 0 {
		capacity = req.GetCapacityRange().GetLimitBytes()
	}
	if req.GetCapacityRange().GetLimitBytes() > 0 && capacity == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume capacity must be greater than zero")
	}
	if apierrors.IsNotFound(err) {
		volume = &storagev1alpha1.SyncthingVolume{
			ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{"syncthing-storage.sorend.github.com/capacity-bytes": fmt.Sprint(capacity)}},
			Spec: storagev1alpha1.SyncthingVolumeSpec{
				VolumeHandle: name,
				FolderID:     name,
				InitialSync: storagev1alpha1.InitialSyncPolicy{
					Policy: req.GetParameters()["initialSync"],
				},
			},
		}
		if retention := req.GetParameters()["replicaRetention"]; retention != "" {
			if _, err := time.ParseDuration(retention); err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "invalid replicaRetention %q: %v", retention, err)
			}
			volume.Spec.Retention = &storagev1alpha1.ReplicaRetention{AfterDetach: retention}
		}
		if volume.Spec.InitialSync.Policy == "" {
			volume.Spec.InitialSync.Policy = "wait"
		}
		if volume.Spec.InitialSync.Policy != "wait" && volume.Spec.InitialSync.Policy != "none" {
			return nil, status.Errorf(codes.InvalidArgument, "unsupported initialSync policy %q", volume.Spec.InitialSync.Policy)
		}
		if err := validateRetention(volume.Spec.Retention); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		if err := s.Client.Create(ctx, volume); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return nil, status.Errorf(codes.Internal, "create SyncthingVolume: %v", err)
			}
			if err := s.Client.Get(ctx, types.NamespacedName{Name: name}, volume); err != nil {
				return nil, status.Errorf(codes.Internal, "get existing SyncthingVolume: %v", err)
			}
			if volume.Spec.VolumeHandle != name || volume.Spec.FolderID != name || volume.Annotations["syncthing-storage.sorend.github.com/capacity-bytes"] != fmt.Sprint(capacity) {
				return nil, status.Error(codes.AlreadyExists, "volume name already exists with a different capacity")
			}
		}
	} else if err != nil {
		return nil, status.Errorf(codes.Internal, "get SyncthingVolume: %v", err)
	} else if !volume.DeletionTimestamp.IsZero() {
		return nil, status.Error(codes.Aborted, "volume is being deleted")
	} else if volume.Spec.InitialSync.Policy != req.GetParameters()["initialSync"] && req.GetParameters()["initialSync"] != "" {
		return nil, status.Error(codes.AlreadyExists, "volume name already exists with a different initial sync policy")
	} else if volume.Spec.InitialSync.Policy != "wait" && volume.Spec.InitialSync.Policy != "none" {
		return nil, status.Error(codes.AlreadyExists, "volume already exists with a different initial sync policy")
	} else if err := validateRetention(volume.Spec.Retention); err != nil {
		return nil, status.Error(codes.AlreadyExists, "volume already exists with invalid retention")
	} else if volume.Spec.VolumeHandle != name || volume.Spec.FolderID != name {
		return nil, status.Error(codes.AlreadyExists, "volume name is already used by a different volume")
	} else if capacity > 0 && volume.Annotations["syncthing-storage.sorend.github.com/capacity-bytes"] != fmt.Sprint(capacity) {
		return nil, status.Error(codes.AlreadyExists, "volume name already exists with a different capacity")
	}
	return &csi.CreateVolumeResponse{Volume: &csi.Volume{
		VolumeId:      volume.Spec.VolumeHandle,
		CapacityBytes: capacity,
		VolumeContext: map[string]string{"folderID": volume.Spec.FolderID},
	}}, nil
}

func validateRetention(retention *storagev1alpha1.ReplicaRetention) error {
	if retention == nil || retention.AfterDetach == "" {
		return nil
	}
	duration, err := time.ParseDuration(retention.AfterDetach)
	if err != nil {
		return fmt.Errorf("invalid replica retention %q: %w", retention.AfterDetach, err)
	}
	if duration < 0 {
		return fmt.Errorf("replica retention cannot be negative")
	}
	return nil
}

func (s *Controller) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	volume, err := s.getVolume(ctx, req.GetVolumeId())
	if apierrors.IsNotFound(err) {
		return &csi.DeleteVolumeResponse{}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get SyncthingVolume: %v", err)
	}
	if !volume.DeletionTimestamp.IsZero() {
		return nil, status.Error(codes.Aborted, "volume is being deleted; waiting for its finalizer")
	}
	if len(volume.Spec.DesiredReplicas) > 0 {
		volume.Spec.DesiredReplicas = nil
		if err := s.Client.Update(ctx, volume); err != nil {
			return nil, status.Errorf(codes.Aborted, "detach replicas before deletion: %v", err)
		}
		return nil, status.Error(codes.Aborted, "waiting for attached replicas to be detached")
	}
	for _, replica := range volume.Status.Replicas {
		if replica.State != "Absent" && replica.State != "Cached" {
			return nil, status.Error(codes.Aborted, "waiting for local replicas to be removed")
		}
	}
	if err := s.Client.Delete(ctx, volume); err != nil && !apierrors.IsNotFound(err) {
		return nil, status.Errorf(codes.Internal, "delete SyncthingVolume: %v", err)
	}
	for {
		if err := s.Client.Get(ctx, types.NamespacedName{Name: volume.Name}, &storagev1alpha1.SyncthingVolume{}); apierrors.IsNotFound(err) {
			return &csi.DeleteVolumeResponse{}, nil
		} else if err != nil {
			return nil, status.Errorf(codes.Internal, "wait for SyncthingVolume deletion: %v", err)
		}
		if err := wait(ctx, s.Poll); err != nil {
			return nil, status.FromContextError(err).Err()
		}
	}
}

func (s *Controller) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	if req.GetVolumeId() == "" || req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID and node ID are required")
	}
	if err := validateCapabilities([]*csi.VolumeCapability{req.GetVolumeCapability()}); err != nil {
		return nil, err
	}
	if req.GetVolumeCapability().GetAccessMode().GetMode() != csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER {
		return nil, status.Error(codes.InvalidArgument, "only ReadWriteOnce attachments are supported")
	}
	volume, err := s.getVolume(ctx, req.GetVolumeId())
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Error(codes.NotFound, "volume not found")
		}
		return nil, status.Errorf(codes.Internal, "get SyncthingVolume: %v", err)
	}
	if !volume.DeletionTimestamp.IsZero() {
		return nil, status.Error(codes.Aborted, "volume is being deleted")
	}
	if volume.Spec.InitialSync.Policy != "wait" && volume.Spec.InitialSync.Policy != "none" {
		return nil, status.Error(codes.FailedPrecondition, "volume has an unsupported initial sync policy")
	}
	node := &storagev1alpha1.SyncthingNode{}
	if err := s.Client.Get(ctx, types.NamespacedName{Name: req.GetNodeId()}, node); err != nil {
		return nil, status.Errorf(codes.NotFound, "get SyncthingNode %q: %v", req.GetNodeId(), err)
	}
	if node.Spec.NodeName != req.GetNodeId() {
		return nil, status.Error(codes.FailedPrecondition, "SyncthingNode name and nodeName must match the CSI node ID")
	}
	if !node.DeletionTimestamp.IsZero() {
		return nil, status.Errorf(codes.Unavailable, "SyncthingNode %q is being deleted", req.GetNodeId())
	}
	if !node.Status.Ready || node.Spec.DeviceID == "" || node.Status.ObservedDeviceID != node.Spec.DeviceID {
		return nil, status.Errorf(codes.Unavailable, "SyncthingNode %q is not ready", req.GetNodeId())
	}
	if node.Status.LastSeen == nil || time.Since(node.Status.LastSeen.Time) > 45*time.Second {
		return nil, status.Errorf(codes.Unavailable, "SyncthingNode %q agent heartbeat is stale", req.GetNodeId())
	}
	if len(node.Spec.Addresses) == 0 {
		return nil, status.Errorf(codes.Unavailable, "SyncthingNode %q has no peer address", req.GetNodeId())
	}
	if volume.Spec.InitialSync.Policy != "none" {
		priorData := false
		for _, replica := range volume.Status.Replicas {
			if replica.NodeName != req.GetNodeId() && replica.State != "Absent" && replica.State != "Cached" {
				priorNode := &storagev1alpha1.SyncthingNode{}
				if err := s.Client.Get(ctx, types.NamespacedName{Name: replica.NodeName}, priorNode); err == nil && priorNode.Status.Ready && priorNode.Status.ObservedDeviceID == priorNode.Spec.DeviceID {
					priorData = true
				}
				break
			}
		}
		if hasPreviouslyReplicatedData(volume) && !priorData && !hasUsableCachedReplica(volume) {
			return nil, status.Error(codes.FailedPrecondition, "no source replica is currently available; refusing to initialize an empty replica")
		}
	}
	if req.GetReadonly() {
		return nil, status.Error(codes.InvalidArgument, "read-only controller attachments are not supported")
	}
	if req.GetVolumeCapability().GetAccessMode().GetMode() != csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER {
		return nil, status.Error(codes.InvalidArgument, "only single-node writer volumes are supported")
	}
	for _, replica := range volume.Spec.DesiredReplicas {
		if replica.NodeName != req.GetNodeId() && replica.Mode == "active" {
			return nil, status.Errorf(codes.Aborted, "volume is already attached to node %q", replica.NodeName)
		}
	}
	if setReplicaMode(volume, req.GetNodeId(), "active") {
		if err := s.Client.Update(ctx, volume); err != nil {
			return nil, status.Errorf(codes.Aborted, "update SyncthingVolume replicas: %v", err)
		}
	}
	for {
		current, err := s.getVolume(ctx, req.GetVolumeId())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "read replica status: %v", err)
		}
		if !current.DeletionTimestamp.IsZero() {
			return nil, status.Error(codes.Aborted, "volume is being deleted")
		}
		for _, replica := range current.Status.Replicas {
			if replica.NodeName == req.GetNodeId() && (replica.State == "Ready" || (current.Spec.InitialSync.Policy == "none" && replica.State != "Absent")) {
				return &csi.ControllerPublishVolumeResponse{PublishContext: map[string]string{"nodeName": req.GetNodeId()}}, nil
			}
		}
		if err := wait(ctx, s.Poll); err != nil {
			return nil, status.FromContextError(err).Err()
		}
	}
}

func hasUsableCachedReplica(volume *storagev1alpha1.SyncthingVolume) bool {
	for _, replica := range volume.Spec.DesiredReplicas {
		if replica.Mode == "cache" {
			for _, status := range volume.Status.Replicas {
				if status.NodeName == replica.NodeName && status.State == "Cached" {
					return true
				}
			}
		}
	}
	return false
}

func hasPreviouslyReplicatedData(volume *storagev1alpha1.SyncthingVolume) bool {
	for _, replica := range volume.Status.Replicas {
		if replica.Completion >= 100 && replica.State != "Absent" {
			return true
		}
	}
	return false
}

func (s *Controller) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	if req.GetVolumeId() == "" || req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID and node ID are required")
	}
	volume, err := s.getVolume(ctx, req.GetVolumeId())
	if apierrors.IsNotFound(err) {
		return &csi.ControllerUnpublishVolumeResponse{}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get SyncthingVolume: %v", err)
	}
	if !volume.DeletionTimestamp.IsZero() {
		for i := range volume.Spec.DesiredReplicas {
			if volume.Spec.DesiredReplicas[i].NodeName == req.GetNodeId() {
				volume.Spec.DesiredReplicas = append(volume.Spec.DesiredReplicas[:i], volume.Spec.DesiredReplicas[i+1:]...)
				if err := s.Client.Update(ctx, volume); err != nil {
					return nil, status.Errorf(codes.Aborted, "remove deleting volume replica: %v", err)
				}
				return nil, status.Error(codes.Aborted, "waiting for deleting volume replica removal")
			}
		}
		for _, replica := range volume.Status.Replicas {
			if replica.NodeName == req.GetNodeId() && replica.State != "Absent" {
				return nil, status.Error(codes.Aborted, "waiting for deleting volume replica removal")
			}
		}
		return &csi.ControllerUnpublishVolumeResponse{}, nil
	}
	if setReplicaMode(volume, req.GetNodeId(), "cache") {
		if err := s.Client.Update(ctx, volume); err != nil {
			return nil, status.Errorf(codes.Aborted, "cache SyncthingVolume replica: %v", err)
		}
	}
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func (*Controller) ControllerGetCapabilities(context.Context, *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{Capabilities: []*csi.ControllerServiceCapability{
		{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME}}},
		{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME}}},
	}}, nil
}

func (s *Controller) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	volume, err := s.getVolume(ctx, req.GetVolumeId())
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, status.Error(codes.NotFound, "volume not found")
		}
		return nil, status.Errorf(codes.Internal, "get SyncthingVolume: %v", err)
	}
	if !volume.DeletionTimestamp.IsZero() {
		return nil, status.Error(codes.Aborted, "volume is being deleted")
	}
	if err := validateCapabilities(req.GetVolumeCapabilities()); err != nil {
		return &csi.ValidateVolumeCapabilitiesResponse{Message: status.Convert(err).Message()}, nil
	}
	for _, key := range []string{"backupClass", "backupPolicy"} {
		if req.GetParameters()[key] != "" {
			return &csi.ValidateVolumeCapabilitiesResponse{Message: "unsupported parameter " + key}, nil
		}
	}
	confirmed := append([]*csi.VolumeCapability(nil), req.GetVolumeCapabilities()...)
	sort.Slice(confirmed, func(i, j int) bool {
		return confirmed[i].GetAccessMode().GetMode() < confirmed[j].GetAccessMode().GetMode()
	})
	return &csi.ValidateVolumeCapabilitiesResponse{Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{VolumeCapabilities: confirmed, VolumeContext: req.GetVolumeContext(), Parameters: req.GetParameters()}}, nil
}

func (s *Controller) getVolume(ctx context.Context, handle string) (*storagev1alpha1.SyncthingVolume, error) {
	if handle == "" {
		return nil, fmt.Errorf("volume ID is required")
	}
	volume := &storagev1alpha1.SyncthingVolume{}
	err := s.Client.Get(ctx, types.NamespacedName{Name: handle}, volume)
	return volume, err
}

func validateCapabilities(capabilities []*csi.VolumeCapability) error {
	if len(capabilities) == 0 {
		return status.Error(codes.InvalidArgument, "at least one volume capability is required")
	}
	for _, capability := range capabilities {
		if capability == nil || capability.GetMount() == nil {
			return status.Error(codes.InvalidArgument, "only filesystem mount volumes are supported")
		}
		mode := capability.GetAccessMode().GetMode()
		if mode != csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER {
			return status.Errorf(codes.InvalidArgument, "unsupported access mode %s; use ReadWriteOnce", mode)
		}
		if capability.GetMount().GetFsType() != "" && capability.GetMount().GetFsType() != "ext4" {
			return status.Errorf(codes.InvalidArgument, "unsupported filesystem type %q; only ext4 is supported", capability.GetMount().GetFsType())
		}
		if capability.GetMount().GetVolumeMountGroup() != "" {
			return status.Error(codes.InvalidArgument, "volume mount groups are not supported")
		}
		if err := validateMountFlags(capability.GetMount().GetMountFlags()); err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
		if hasMountFlag(capability.GetMount().GetMountFlags(), "ro") {
			return status.Error(codes.InvalidArgument, "read-only mount flags are not supported")
		}
	}
	return nil
}

func setReplicaMode(volume *storagev1alpha1.SyncthingVolume, nodeName, mode string) bool {
	for i := range volume.Spec.DesiredReplicas {
		if volume.Spec.DesiredReplicas[i].NodeName == nodeName {
			if volume.Spec.DesiredReplicas[i].Mode == mode {
				return false
			}
			volume.Spec.DesiredReplicas[i].Mode = mode
			return true
		}
	}
	volume.Spec.DesiredReplicas = append(volume.Spec.DesiredReplicas, storagev1alpha1.DesiredReplica{NodeName: nodeName, Mode: mode})
	return true
}

func safeVolumeName(name string) string {
	original := name
	name = strings.ToLower(name)
	var result strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		} else {
			result.WriteByte('-')
		}
	}
	name = strings.Trim(result.String(), "-")
	if len(name) > 54 {
		name = name[:54]
	}
	if name == "" {
		return "volume"
	}
	digest := sha256.Sum256([]byte(original))
	name += "-" + hex.EncodeToString(digest[:4])
	if name[0] < 'a' || name[0] > 'z' {
		name = "v-" + name
	}
	return name
}

func volumeLabel(name string) string {
	return "syncthing-volume:" + name
}

var dnsLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

func wait(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
