package csi

import (
	"context"
	"errors"
	"testing"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	storagev1alpha1 "github.com/sorend/csi-driver-syncthing/api/v1alpha1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCreateVolumeIsIdempotentAndKeepsDistinctNames(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	controller := &Controller{Client: client}
	request := &csi.CreateVolumeRequest{
		Name:          "App/Data",
		CapacityRange: &csi.CapacityRange{RequiredBytes: 1024},
		VolumeCapabilities: []*csi.VolumeCapability{{
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
		}},
	}
	first, err := controller.CreateVolume(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := controller.CreateVolume(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Volume.VolumeId != second.Volume.VolumeId {
		t.Fatalf("idempotent volume IDs differ: %q != %q", first.Volume.VolumeId, second.Volume.VolumeId)
	}
	volume := &storagev1alpha1.SyncthingVolume{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: first.Volume.VolumeId}, volume); err != nil {
		t.Fatal(err)
	}
	if volume.Spec.InitialSync.Policy != "wait" {
		t.Fatalf("initial sync policy = %q, want wait", volume.Spec.InitialSync.Policy)
	}
	if safeVolumeName("app/data") == safeVolumeName("app-data") {
		t.Fatal("distinct CSI names mapped to the same Kubernetes object name")
	}
	if volumeLabel("app/data") != "syncthing-volume:app/data" {
		t.Fatal("volume label lost the original CSI name")
	}
}

func TestCreateVolumeRejectsDifferentCapacityOnRetry(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	controller := &Controller{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	request := func(capacity int64) *csi.CreateVolumeRequest {
		return &csi.CreateVolumeRequest{
			Name: "same-name", CapacityRange: &csi.CapacityRange{RequiredBytes: capacity},
			VolumeCapabilities: []*csi.VolumeCapability{{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}, AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}}},
		}
	}
	if _, err := controller.CreateVolume(context.Background(), request(1024)); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.CreateVolume(context.Background(), request(2048)); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("CreateVolume retry error = %v, want AlreadyExists", err)
	}
}

func TestControllerPublishRequiresReadyNodeAndAddsReplica(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	volume := &storagev1alpha1.SyncthingVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "volume"},
		Spec:       storagev1alpha1.SyncthingVolumeSpec{VolumeHandle: "volume", FolderID: "volume", InitialSync: storagev1alpha1.InitialSyncPolicy{Policy: "none"}},
	}
	node := &storagev1alpha1.SyncthingNode{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-a"},
		Spec:       storagev1alpha1.SyncthingNodeSpec{NodeName: "worker-a", DeviceID: "device-a", Addresses: []string{"tcp://192.0.2.1:22000"}},
		Status:     storagev1alpha1.SyncthingNodeStatus{Ready: true, ObservedDeviceID: "device-a", LastSeen: &metav1.Time{Time: time.Now()}},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(volume, node).WithObjects(volume, node).Build()
	controller := &Controller{Client: client, Poll: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := controller.ControllerPublishVolume(ctx, &csi.ControllerPublishVolumeRequest{
		VolumeId: "volume", NodeId: "worker-a",
		VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}, AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}},
	})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("ControllerPublishVolume error = %v, want deadline while waiting for node agent", err)
	}
	got := &storagev1alpha1.SyncthingVolume{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: "volume"}, got); err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.DesiredReplicas) != 1 || got.Spec.DesiredReplicas[0].NodeName != "worker-a" {
		t.Fatalf("desired replicas = %v", got.Spec.DesiredReplicas)
	}
}

func TestControllerPublishUpdatesOnlyVolumeSpec(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	volume := &storagev1alpha1.SyncthingVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "volume", Generation: 2},
		Spec: storagev1alpha1.SyncthingVolumeSpec{
			VolumeHandle: "volume", FolderID: "volume",
			InitialSync: storagev1alpha1.InitialSyncPolicy{Policy: "none"},
		},
		Status: storagev1alpha1.SyncthingVolumeStatus{ObservedGeneration: 1},
	}
	node := &storagev1alpha1.SyncthingNode{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-a"},
		Spec: storagev1alpha1.SyncthingNodeSpec{
			NodeName: "worker-a", DeviceID: "device-a", Addresses: []string{"tcp://192.0.2.1:22000"},
		},
		Status: storagev1alpha1.SyncthingNodeStatus{
			Ready: true, ObservedDeviceID: "device-a", LastSeen: &metav1.Time{Time: time.Now()},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(volume, node).WithObjects(volume, node).Build()
	controller := &Controller{Client: client, Reader: client, Poll: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := controller.ControllerPublishVolume(ctx, &csi.ControllerPublishVolumeRequest{
		VolumeId: "volume", NodeId: "worker-a",
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
		},
	})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("ControllerPublishVolume error = %v, want deadline waiting for agent status", err)
	}
	got := &storagev1alpha1.SyncthingVolume{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: "volume"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.ObservedGeneration != 1 {
		t.Fatalf("observed generation = %d, controller spec patch overwrote status", got.Status.ObservedGeneration)
	}
	if len(got.Spec.DesiredReplicas) != 1 || got.Spec.DesiredReplicas[0].Mode != "active" {
		t.Fatalf("desired replicas = %v, want worker-a active", got.Spec.DesiredReplicas)
	}
}

type conflictOnceClient struct {
	client.Client
	conflict bool
}

func (c *conflictOnceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if !c.conflict {
		c.conflict = true
		volume := &storagev1alpha1.SyncthingVolume{}
		if err := c.Client.Get(ctx, types.NamespacedName{Name: obj.GetName()}, volume); err != nil {
			return err
		}
		volume.Status.Replicas = []storagev1alpha1.ReplicaStatus{{NodeName: "worker-a", State: "Ready"}}
		if err := c.Client.Status().Update(ctx, volume); err != nil {
			return err
		}
		c.conflict = true
		return apierrors.NewConflict(schema.GroupResource{Resource: "syncthingvolumes"}, obj.GetName(), errors.New("simulated concurrent update"))
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func TestUpdateVolumeRetriesConflictWithoutOverwritingStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	volume := &storagev1alpha1.SyncthingVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "volume"},
		Spec:       storagev1alpha1.SyncthingVolumeSpec{VolumeHandle: "volume", FolderID: "volume"},
	}
	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(volume).WithObjects(volume).Build()
	controller := &Controller{Client: &conflictOnceClient{Client: baseClient}}
	changed, err := controller.updateVolume(context.Background(), "volume", func(current *storagev1alpha1.SyncthingVolume) (bool, error) {
		return setReplicaMode(current, "worker-a", "active"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("updateVolume reported no change")
	}
	got := &storagev1alpha1.SyncthingVolume{}
	if err := baseClient.Get(context.Background(), types.NamespacedName{Name: "volume"}, got); err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.DesiredReplicas) != 1 || got.Spec.DesiredReplicas[0].Mode != "active" {
		t.Fatalf("desired replicas = %v, want worker-a active", got.Spec.DesiredReplicas)
	}
	if len(got.Status.Replicas) != 1 || got.Status.Replicas[0].State != "Ready" {
		t.Fatalf("status replicas = %v, concurrent status update was lost", got.Status.Replicas)
	}
}

func TestValidateVolumeCapabilitiesRejectsEmptyCapabilities(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	volume := &storagev1alpha1.SyncthingVolume{ObjectMeta: metav1.ObjectMeta{Name: "volume"}}
	controller := &Controller{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(volume).Build()}
	_, err := controller.ValidateVolumeCapabilities(context.Background(), &csi.ValidateVolumeCapabilitiesRequest{VolumeId: "volume"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("ValidateVolumeCapabilities error = %v, want InvalidArgument", err)
	}
}

func TestControllerPublishRejectsVolumeWithoutAvailableSource(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	volume := &storagev1alpha1.SyncthingVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "volume", Generation: 2},
		Spec:       storagev1alpha1.SyncthingVolumeSpec{VolumeHandle: "volume", FolderID: "volume", InitialSync: storagev1alpha1.InitialSyncPolicy{Policy: "wait"}},
		Status:     storagev1alpha1.SyncthingVolumeStatus{Replicas: []storagev1alpha1.ReplicaStatus{{NodeName: "lost-node", State: "Ready", Completion: 100}}},
	}
	node := &storagev1alpha1.SyncthingNode{
		ObjectMeta: metav1.ObjectMeta{Name: "new-node"},
		Spec:       storagev1alpha1.SyncthingNodeSpec{NodeName: "new-node", DeviceID: "new-id", Addresses: []string{"tcp://192.0.2.2:22000"}},
		Status:     storagev1alpha1.SyncthingNodeStatus{Ready: true, ObservedDeviceID: "new-id", LastSeen: &metav1.Time{Time: time.Now()}},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(volume, node).WithObjects(volume, node).Build()
	controller := &Controller{Client: client}
	_, err := controller.ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{
		VolumeId: "volume", NodeId: "new-node",
		VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}, AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("ControllerPublishVolume error = %v, want FailedPrecondition", err)
	}
}

func TestControllerPublishRejectsStaleNodeHeartbeat(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	volume := &storagev1alpha1.SyncthingVolume{ObjectMeta: metav1.ObjectMeta{Name: "volume"}, Spec: storagev1alpha1.SyncthingVolumeSpec{VolumeHandle: "volume", FolderID: "volume", InitialSync: storagev1alpha1.InitialSyncPolicy{Policy: "none"}}}
	node := &storagev1alpha1.SyncthingNode{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-a"},
		Spec:       storagev1alpha1.SyncthingNodeSpec{NodeName: "worker-a", DeviceID: "device-a", Addresses: []string{"tcp://192.0.2.1:22000"}},
		Status:     storagev1alpha1.SyncthingNodeStatus{Ready: true, ObservedDeviceID: "device-a", LastSeen: &metav1.Time{Time: time.Now().Add(-time.Minute)}},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(volume, node).WithObjects(volume, node).Build()
	controller := &Controller{Client: client}
	_, err := controller.ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{
		VolumeId: "volume", NodeId: "worker-a",
		VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}, AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}},
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("ControllerPublishVolume error = %v, want Unavailable", err)
	}
}

func TestValidateCapabilitiesRejectsMultiWriter(t *testing.T) {
	err := validateCapabilities([]*csi.VolumeCapability{{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
	}})
	if err == nil {
		t.Fatal("multi-writer volume capability was accepted")
	}
}

func TestValidateCapabilitiesRejectsUnknownFilesystem(t *testing.T) {
	err := validateCapabilities([]*csi.VolumeCapability{{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: "xfs"}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
	}})
	if err == nil {
		t.Fatal("unsupported filesystem type was accepted")
	}
}

func TestSafeVolumeNameStartsWithLetter(t *testing.T) {
	name := safeVolumeName("1data")
	if !dnsLabelPattern.MatchString(name) || name[0] < 'a' || name[0] > 'z' {
		t.Fatalf("safeVolumeName returned invalid Kubernetes name %q", name)
	}
}

func TestStorageClassParametersRejectUnknownAndInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]string
	}{
		{name: "bad sync", params: map[string]string{"initialSync": "eventual"}},
		{name: "bad retention", params: map[string]string{"replicaRetention": "forever"}},
		{name: "negative retention", params: map[string]string{"replicaRetention": "-1h"}},
		{name: "backup unsupported", params: map[string]string{"backupClass": "external"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := storagev1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			controller := &Controller{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
			_, err := controller.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
				Name: test.name, CapacityRange: &csi.CapacityRange{RequiredBytes: 1024}, Parameters: test.params,
				VolumeCapabilities: []*csi.VolumeCapability{{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}, AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}}},
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("CreateVolume error = %v, want InvalidArgument", err)
			}
		})
	}
}
