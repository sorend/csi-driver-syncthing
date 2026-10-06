package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	storagev1alpha1 "github.com/sorend/csi-driver-syncthing/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Agent struct {
	Client     client.Client
	Syncthing  *Syncthing
	NodeName   string
	NodeIP     string
	VolumesDir string
	MountsDir  string
	Interval   time.Duration
	mu         sync.Mutex
}

var volumeHandlePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

func (a *Agent) Run(ctx context.Context) error {
	if a.NodeName == "" || a.Syncthing == nil {
		return fmt.Errorf("node name and Syncthing client are required")
	}
	if a.VolumesDir == "" {
		a.VolumesDir = "/var/lib/csi-syncthing/volumes"
	}
	if a.Interval <= 0 {
		a.Interval = 10 * time.Second
	}
	if a.MountsDir == "" {
		a.MountsDir = "/var/lib/csi-syncthing/mounts"
	}
	for {
		if err := a.Reconcile(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "agent reconciliation failed: %v\n", err)
		}
		timer := time.NewTimer(a.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (a *Agent) Reconcile(ctx context.Context) error {
	if a.NodeName == "" || a.Syncthing == nil {
		return fmt.Errorf("node name and Syncthing client are required")
	}
	if a.NodeName == "" || a.NodeIP == "" || a.Syncthing == nil {
		return fmt.Errorf("node name, node IP, and Syncthing client are required")
	}
	status, err := a.Syncthing.Status(ctx)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ctx.Err()
		}
		return fmt.Errorf("read Syncthing identity: %w", err)
	}
	if status.MyID == "" {
		return fmt.Errorf("Syncthing returned an empty device ID")
	}
	if err := a.ensureNodeStateDirectories(); err != nil {
		return err
	}
	if err := a.publishNode(ctx, status.MyID); err != nil {
		return err
	}
	version, err := a.Syncthing.Version(ctx)
	if err != nil {
		return fmt.Errorf("read Syncthing version: %w", err)
	}
	if err := a.publishNodeVersion(ctx, version); err != nil {
		return err
	}
	if err := a.reconcileSyncthing(ctx); err != nil {
		return err
	}
	if err := a.reconcileSyncthing(ctx); err != nil {
		return err
	}
	volumes := &storagev1alpha1.SyncthingVolumeList{}
	if err := a.Client.List(ctx, volumes); err != nil {
		return fmt.Errorf("list SyncthingVolumes: %w", err)
	}
	if err := a.reconcileOrphanFolders(ctx, volumes.Items, status.MyID); err != nil {
		return err
	}
	nodes := &storagev1alpha1.SyncthingNodeList{}
	if err := a.Client.List(ctx, nodes); err != nil {
		return fmt.Errorf("list SyncthingNodes: %w", err)
	}
	knownNodes := make(map[string]storagev1alpha1.SyncthingNode, len(nodes.Items))
	for _, node := range nodes.Items {
		knownNodes[node.Name] = node
	}
	for i := range volumes.Items {
		if err := a.reconcileVolume(ctx, status.MyID, &volumes.Items[i], knownNodes); err != nil {
			fmt.Fprintf(os.Stderr, "reconcile volume %q: %v\n", volumes.Items[i].Name, err)
		}
	}
	return nil
}

func (a *Agent) ensureNodeStateDirectories() error {
	for _, path := range []string{a.VolumesDir, a.MountsDir} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("state directory %q must be a clean absolute path", path)
		}
		if err := os.MkdirAll(path, 0755); err != nil {
			return fmt.Errorf("create node state directory %q: %w", path, err)
		}
	}
	return nil
}

func (a *Agent) reconcileSyncthing(ctx context.Context) error {
	config, err := a.Syncthing.Config(ctx)
	if err != nil {
		return fmt.Errorf("read Syncthing config: %w", err)
	}
	options, ok := config["options"].(map[string]any)
	if !ok {
		return fmt.Errorf("Syncthing config has no options object")
	}
	changed := false
	for _, key := range []string{"localAnnounceEnabled", "globalAnnounceEnabled", "relaysEnabled", "natEnabled"} {
		if enabled, ok := options[key].(bool); !ok || enabled {
			options[key] = false
			changed = true
		}
	}
	if addresses, ok := options["listenAddresses"].([]any); ok {
		if len(addresses) != 1 || addresses[0] != "tcp://0.0.0.0:22000" {
			options["listenAddresses"] = []string{"tcp://0.0.0.0:22000"}
			changed = true
		}
	}
	if changed {
		if err := a.Syncthing.UpdateConfig(ctx, config); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("disable Syncthing discovery and relays: %w", err)
		}
	}
	return nil
}

func (a *Agent) publishNodeVersion(ctx context.Context, version string) error {
	node := &storagev1alpha1.SyncthingNode{}
	if err := a.Client.Get(ctx, types.NamespacedName{Name: a.NodeName}, node); err != nil {
		return err
	}
	before := node.DeepCopy()
	node.Status.SyncthingVersion = version
	node.Status.Ready = true
	node.Status.ObservedDeviceID = node.Spec.DeviceID
	now := metav1.Now()
	node.Status.LastSeen = &now
	if err := a.Client.Status().Patch(ctx, node, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("publish Syncthing version: %w", err)
	}
	return nil
}

func (a *Agent) reconcileOrphanFolders(ctx context.Context, volumes []storagev1alpha1.SyncthingVolume, localID string) error {
	known := make(map[string]struct{}, len(volumes))
	for _, volume := range volumes {
		known[volume.Spec.FolderID] = struct{}{}
	}
	folders, err := a.Syncthing.Folders(ctx)
	if err != nil {
		return fmt.Errorf("list configured Syncthing folders: %w", err)
	}
	for _, folder := range folders {
		if !strings.HasPrefix(folder.Label, "syncthing-volume:") {
			continue
		}
		if _, participant := folderDevice(folder.Devices, localID); !participant {
			continue
		}
		if _, ok := known[folder.ID]; ok {
			continue
		}
		mounted, err := a.hasPublishedMount(folder.ID)
		if err != nil {
			return err
		}
		if mounted {
			continue
		}
		if err := a.Syncthing.RemoveFolder(ctx, folder.ID); err != nil {
			return fmt.Errorf("remove orphaned Syncthing folder %q: %w", folder.ID, err)
		}
		if err := os.RemoveAll(filepath.Join(a.VolumesDir, folder.ID)); err != nil {
			return fmt.Errorf("remove orphaned volume directory %q: %w", folder.ID, err)
		}
	}
	entries, err := os.ReadDir(a.VolumesDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("list local volume directory: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() == ".ownership" {
			continue
		}
		volumePath := filepath.Join(a.VolumesDir, entry.Name())
		knownVolume := false
		for _, volume := range volumes {
			if volume.Spec.VolumeHandle == entry.Name() {
				knownVolume = true
				break
			}
		}
		if knownVolume {
			continue
		}
		markerPath := filepath.Join(a.VolumesDir, ".ownership", entry.Name())
		marker, err := os.ReadFile(markerPath)
		if err != nil || string(marker) != localID {
			continue
		}
		if mounted, err := a.hasPublishedMount(entry.Name()); err != nil {
			return err
		} else if mounted || hasMountedChild("/var/lib/kubelet/pods", entry.Name()) {
			continue
		}
		if err := os.RemoveAll(volumePath); err != nil {
			return fmt.Errorf("remove orphaned local volume %q: %w", entry.Name(), err)
		}
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove orphaned ownership marker %q: %w", entry.Name(), err)
		}
	}
	return nil
}

func folderDevice(devices []FolderDevice, id string) (FolderDevice, bool) {
	for _, device := range devices {
		if device.DeviceID == id {
			return device, true
		}
	}
	return FolderDevice{}, false
}

func (a *Agent) publishNode(ctx context.Context, deviceID string) error {
	node := &storagev1alpha1.SyncthingNode{}
	err := a.Client.Get(ctx, types.NamespacedName{Name: a.NodeName}, node)
	if apierrors.IsNotFound(err) {
		node = &storagev1alpha1.SyncthingNode{}
		node.Name = a.NodeName
		node.Spec.NodeName = a.NodeName
		node.Spec.DeviceID = deviceID
		node.Spec.Addresses = []string{"tcp://" + net.JoinHostPort(a.NodeIP, "22000")}
		if err := a.Client.Create(ctx, node); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create SyncthingNode: %w", err)
		}
		return a.publishNode(ctx, deviceID)
	}
	if err != nil {
		return fmt.Errorf("get SyncthingNode: %w", err)
	}
	if node.Spec.NodeName != "" && node.Spec.NodeName != a.NodeName {
		return fmt.Errorf("SyncthingNode name %q does not match Kubernetes node %q", node.Spec.NodeName, a.NodeName)
	}
	if !node.DeletionTimestamp.IsZero() {
		return fmt.Errorf("SyncthingNode %q is being deleted", a.NodeName)
	}
	if node.Spec.NodeName != "" && node.Spec.NodeName != a.NodeName {
		return fmt.Errorf("SyncthingNode %q maps to Kubernetes node %q", a.NodeName, node.Spec.NodeName)
	}
	if node.Status.ObservedDeviceID != "" && node.Status.ObservedDeviceID != deviceID {
		return fmt.Errorf("Syncthing identity changed on node %q; refusing to replace %s with %s", a.NodeName, node.Status.ObservedDeviceID, deviceID)
	}
	if node.Status.ObservedDeviceID == "" && node.Spec.DeviceID == "" {
		before := node.DeepCopy()
		node.Spec.DeviceID = deviceID
		node.Spec.NodeName = a.NodeName
		node.Spec.Addresses = []string{"tcp://" + net.JoinHostPort(a.NodeIP, "22000")}
		if err := a.Client.Patch(ctx, node, client.MergeFrom(before)); err != nil {
			return fmt.Errorf("initialize SyncthingNode identity: %w", err)
		}
		return nil
	}
	if node.Status.ObservedDeviceID == "" && node.Spec.DeviceID != "" && node.Spec.DeviceID != deviceID {
		return fmt.Errorf("SyncthingNode %q device ID does not match the local daemon", a.NodeName)
	}
	if node.Spec.DeviceID != "" && node.Spec.DeviceID != deviceID {
		return fmt.Errorf("SyncthingNode %q already has a different device ID", a.NodeName)
	}
	if node.Status.ObservedDeviceID == deviceID && !node.Status.Ready {
		before := node.DeepCopy()
		node.Status.Ready = true
		now := metav1.Now()
		node.Status.LastSeen = &now
		if err := a.Client.Status().Patch(ctx, node, client.MergeFrom(before)); err != nil {
			return fmt.Errorf("restore SyncthingNode readiness: %w", err)
		}
	}
	before := node.DeepCopy()
	node.Spec.NodeName = a.NodeName
	node.Spec.DeviceID = deviceID
	node.Spec.Addresses = []string{"tcp://" + net.JoinHostPort(a.NodeIP, "22000")}
	if err := a.Client.Patch(ctx, node, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("update SyncthingNode: %w", err)
	}
	return nil
}

func (a *Agent) reconcileVolume(ctx context.Context, localID string, volume *storagev1alpha1.SyncthingVolume, nodes map[string]storagev1alpha1.SyncthingNode) error {
	desired := false
	for _, replica := range volume.Spec.DesiredReplicas {
		if replica.NodeName == a.NodeName {
			desired = true
			break
		}
	}
	path := filepath.Join(a.VolumesDir, volume.Spec.VolumeHandle)
	if !filepath.IsAbs(a.VolumesDir) || filepath.Clean(a.VolumesDir) != a.VolumesDir || !strings.HasPrefix(a.VolumesDir, "/var/lib/csi-syncthing/") {
		return fmt.Errorf("volume root %q must be under /var/lib/csi-syncthing", a.VolumesDir)
	}
	if volume.Spec.VolumeHandle == "" || volume.Spec.FolderID == "" || filepath.Base(path) != volume.Spec.VolumeHandle || volume.Spec.VolumeHandle == "." || volume.Spec.VolumeHandle == ".." {
		return fmt.Errorf("volume %q has an invalid handle or folder ID", volume.Name)
	}
	if len(volume.Spec.VolumeHandle) > 63 || !volumeHandlePattern.MatchString(volume.Spec.VolumeHandle) {
		return fmt.Errorf("volume %q has an invalid Kubernetes-compatible handle", volume.Name)
	}
	if volume.Name != volume.Spec.VolumeHandle || volume.Spec.FolderID != volume.Spec.VolumeHandle {
		return fmt.Errorf("volume %q has a handle or folder ID inconsistent with the storage path", volume.Name)
	}
	localNode, exists := nodes[a.NodeName]
	if desired && (!exists || !localNode.Status.Ready || localNode.Spec.DeviceID != localID || localNode.Status.ObservedDeviceID != localID) {
		return fmt.Errorf("SyncthingNode %q is not ready or its identity changed", a.NodeName)
	}
	mode := ""
	for _, replica := range volume.Spec.DesiredReplicas {
		if replica.NodeName == a.NodeName {
			mode = replica.Mode
			break
		}
	}
	if volume.Spec.Retention != nil && volume.Spec.Retention.AfterDetach != "" {
		if _, err := time.ParseDuration(volume.Spec.Retention.AfterDetach); err != nil {
			return fmt.Errorf("invalid replica retention %q", volume.Spec.Retention.AfterDetach)
		}
	}
	if volume.Spec.InitialSync.Policy != "wait" && volume.Spec.InitialSync.Policy != "none" {
		return fmt.Errorf("unsupported initial sync policy %q", volume.Spec.InitialSync.Policy)
	}
	if !desired {
		if volume.DeletionTimestamp.IsZero() {
			replica, exists := replicaForNode(volume, a.NodeName)
			if !exists || replica.State == "Absent" || replica.State == "Cached" {
				return nil
			}
			return a.removeLocalReplica(ctx, localID, volume, path)
		}
		if _, exists := replicaForNode(volume, a.NodeName); !exists {
			return nil
		}
		mounted, err := a.hasPublishedMount(volume.Spec.VolumeHandle)
		if err != nil {
			return err
		}
		if mounted || hasMountedChild(filepath.Join("/var/lib/kubelet", "pods"), volume.Spec.VolumeHandle) {
			return nil
		}
		if replica, exists := replicaForNode(volume, a.NodeName); !exists || replica.State == "Absent" {
			return nil
		}
		return a.removeLocalReplica(ctx, localID, volume, path)
	}
	if mode != "cache" {
		retention := time.Duration(0)
		if volume.Spec.Retention != nil && volume.Spec.Retention.AfterDetach != "" {
			var err error
			retention, err = time.ParseDuration(volume.Spec.Retention.AfterDetach)
			if err != nil || retention < 0 {
				return fmt.Errorf("invalid replica retention %q", volume.Spec.Retention.AfterDetach)
			}
		}
		if cached, ok := replicaForNode(volume, a.NodeName); ok && cached.State == "Cached" && retention > 0 && cached.LastTransitionTime != nil && time.Since(cached.LastTransitionTime.Time) >= retention {
			return a.expireCachedReplica(ctx, localID, volume, path)
		}
	}
	if mode == "cache" {
		mounted, err := a.hasPublishedMount(volume.Spec.VolumeHandle)
		if err != nil {
			return err
		}
		if mounted {
			return nil
		}
		retention := time.Duration(0)
		if volume.Spec.Retention != nil {
			if volume.Spec.Retention.AfterDetach != "" {
				retention, err = time.ParseDuration(volume.Spec.Retention.AfterDetach)
				if err != nil || retention < 0 {
					return fmt.Errorf("invalid replica retention %q", volume.Spec.Retention.AfterDetach)
				}
			}
		}
		previous, exists := replicaForNode(volume, a.NodeName)
		if exists && previous.State == "Cached" && (retention <= 0 || (previous.LastTransitionTime != nil && time.Since(previous.LastTransitionTime.Time) >= retention)) {
			return a.expireCachedReplica(ctx, localID, volume, path)
		}
		if exists && previous.State != "Cached" {
			if err := a.Syncthing.EnsureFolder(ctx, Folder{ID: volume.Spec.FolderID, Label: "syncthing-volume:" + volume.Name, Path: path, Type: "sendreceive"}); err != nil {
				return fmt.Errorf("retain Syncthing folder: %w", err)
			}
			if err := a.Syncthing.SetFolderPause(ctx, volume.Spec.FolderID, false); err != nil {
				return fmt.Errorf("resume retained Syncthing folder: %w", err)
			}
			return a.publishReplica(ctx, volume, storagev1alpha1.ReplicaStatus{NodeName: a.NodeName, State: "Cached"})
		}
		devices := make([]Device, 0, len(volume.Spec.DesiredReplicas))
		for _, replica := range volume.Spec.DesiredReplicas {
			if replica.NodeName == a.NodeName {
				continue
			}
			peer, ok := nodes[replica.NodeName]
			if !ok || peer.Spec.DeviceID == "" {
				return fmt.Errorf("SyncthingNode %q is unavailable", replica.NodeName)
			}
			address := a.peerAddress(peer)
			if address == "" {
				return fmt.Errorf("SyncthingNode %q has no usable TCP peer address", peer.Name)
			}
			device := Device{DeviceID: peer.Spec.DeviceID, Name: "kube-" + peer.Name, Addresses: []string{address}}
			if err := a.Syncthing.EnsureDevice(ctx, device); err != nil {
				return fmt.Errorf("configure cached peer %q: %w", peer.Name, err)
			}
			devices = append(devices, device)
		}
		folderDevices := make([]FolderDevice, 0, len(devices))
		for _, device := range devices {
			folderDevices = append(folderDevices, FolderDevice{DeviceID: device.DeviceID})
		}
		if err := a.Syncthing.EnsureFolder(ctx, Folder{ID: volume.Spec.FolderID, Label: "syncthing-volume:" + volume.Name, Path: path, Type: "sendreceive", Devices: folderDevices}); err != nil {
			return fmt.Errorf("cache Syncthing folder: %w", err)
		}
		if err := a.Syncthing.SetFolderPause(ctx, volume.Spec.FolderID, false); err != nil {
			return fmt.Errorf("resume cached Syncthing folder: %w", err)
		}
		return a.publishReplica(ctx, volume, storagev1alpha1.ReplicaStatus{NodeName: a.NodeName, State: "Cached"})
	}
	if err := os.MkdirAll(path, 0777); err != nil {
		return fmt.Errorf("create local folder path: %w", err)
	}
	if info, err := os.Stat(filepath.Join(path, ".syncthing-volume")); err == nil && info.IsDir() {
		return fmt.Errorf("volume ownership marker is a directory")
	}
	if err := os.MkdirAll(filepath.Join(a.VolumesDir, ".ownership"), 0775); err != nil {
		return fmt.Errorf("create local ownership directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(a.VolumesDir, ".ownership", volume.Spec.VolumeHandle), []byte(localID), 0664); err != nil {
		return fmt.Errorf("write local volume ownership marker: %w", err)
	}
	devices := make([]Device, 0, len(volume.Spec.DesiredReplicas))
	for _, replica := range volume.Spec.DesiredReplicas {
		if replica.NodeName == a.NodeName {
			continue
		}
		peer, ok := nodes[replica.NodeName]
		if !ok || peer.Spec.DeviceID == "" {
			return fmt.Errorf("SyncthingNode %q is unavailable", replica.NodeName)
		}
		address := a.peerAddress(peer)
		if address == "" {
			return fmt.Errorf("SyncthingNode %q has no usable TCP peer address", peer.Name)
		}
		device := Device{DeviceID: peer.Spec.DeviceID, Name: "kube-" + peer.Name, Addresses: []string{address}}
		if err := a.Syncthing.EnsureDevice(ctx, device); err != nil {
			return fmt.Errorf("configure peer %q: %w", peer.Name, err)
		}
		devices = append(devices, device)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].DeviceID < devices[j].DeviceID })
	folderDevices := make([]FolderDevice, 0, len(devices))
	for _, device := range devices {
		folderDevices = append(folderDevices, FolderDevice{DeviceID: device.DeviceID})
	}
	if err := a.Syncthing.EnsureFolder(ctx, Folder{ID: volume.Spec.FolderID, Label: "syncthing-volume:" + volume.Name, Path: path, Type: "sendreceive", Devices: folderDevices}); err != nil {
		return fmt.Errorf("configure Syncthing folder: %w", err)
	}
	paused := volume.Spec.InitialSync.Policy == "none"
	if err := a.Syncthing.SetFolderPause(ctx, volume.Spec.FolderID, paused); err != nil {
		return fmt.Errorf("set Syncthing folder pause policy: %w", err)
	}
	if paused {
		return a.publishReplica(ctx, volume, storagev1alpha1.ReplicaStatus{NodeName: a.NodeName, State: "Ready", Completion: 100})
	}
	completion := Completion{}
	if volume.Spec.InitialSync.Policy == "wait" {
		var err error
		completion, err = a.Syncthing.Completion(ctx, volume.Spec.FolderID)
		if err != nil {
			return fmt.Errorf("read Syncthing completion: %w", err)
		}
	}
	if volume.Spec.InitialSync.Policy == "wait" && completion.NeedBytes == 0 && completion.NeedItems == 0 {
		if err := a.Syncthing.SetFolderPause(ctx, volume.Spec.FolderID, false); err != nil {
			return fmt.Errorf("resume Syncthing folder after initial sync: %w", err)
		}
	}
	folderStatus, err := a.Syncthing.FolderStatus(ctx, volume.Spec.FolderID)
	if err != nil {
		return fmt.Errorf("read Syncthing folder status: %w", err)
	}
	connected, err := a.peerConnected(ctx, localID, devices)
	if err != nil {
		return err
	}
	hasExistingReplica := false
	for _, replica := range volume.Status.Replicas {
		if replica.NodeName != a.NodeName && replica.State != "Absent" {
			hasExistingReplica = true
			break
		}
	}
	ready := completion.NeedBytes == 0 && completion.NeedItems == 0 && (connected || !hasExistingReplica || volume.Spec.InitialSync.Policy == "none")
	state := "Syncing"
	if folderStatus.State == "scanning" {
		state = "Configuring"
	} else if !connected && len(devices) > 0 {
		state = "Connecting"
	} else if len(devices) == 0 && hasExistingReplica && volume.Spec.InitialSync.Policy != "none" {
		state = "Error"
	} else if ready {
		state = "Ready"
	}
	if volume.Spec.InitialSync.Policy == "none" && state != "Ready" {
		state = "Ready"
	}
	return a.publishReplica(ctx, volume, storagev1alpha1.ReplicaStatus{
		NodeName: a.NodeName, State: state, Completion: ParseCompletion(completion), NeedBytes: completion.NeedBytes,
		NeedItems: completion.NeedItems, Connected: connected,
	})
}

func (a *Agent) peerAddress(node storagev1alpha1.SyncthingNode) string {
	for _, address := range node.Spec.Addresses {
		if strings.HasPrefix(address, "tcp://") {
			return address
		}
	}
	if a.NodeIP != "" && node.Spec.NodeName == a.NodeName {
		return "tcp://" + net.JoinHostPort(a.NodeIP, "22000")
	}
	return ""
}

func (a *Agent) removeLocalReplica(ctx context.Context, localID string, volume *storagev1alpha1.SyncthingVolume, path string) error {
	if mounted, err := a.hasPublishedMount(volume.Spec.VolumeHandle); err != nil {
		return err
	} else if mounted || hasMountedChild("/var/lib/kubelet/pods", volume.Spec.VolumeHandle) {
		return fmt.Errorf("volume %q still has published mounts", volume.Name)
	}
	if err := a.Syncthing.RemoveFolder(ctx, volume.Spec.FolderID); err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove local volume data: %w", err)
	}
	if err := os.Remove(filepath.Join(a.VolumesDir, ".ownership", volume.Spec.VolumeHandle)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove local volume ownership marker: %w", err)
	}
	root := a.MountsDir
	if root == "" {
		root = "/var/lib/csi-syncthing/mounts"
	}
	if err := os.RemoveAll(filepath.Join(root, volume.Spec.VolumeHandle)); err != nil {
		return fmt.Errorf("remove node mount references: %w", err)
	}
	return a.publishReplica(ctx, volume, storagev1alpha1.ReplicaStatus{NodeName: a.NodeName, State: "Absent"})
}

func (a *Agent) expireCachedReplica(ctx context.Context, localID string, volume *storagev1alpha1.SyncthingVolume, path string) error {
	if err := a.removeLocalReplica(ctx, localID, volume, path); err != nil {
		return err
	}
	current := &storagev1alpha1.SyncthingVolume{}
	if err := a.Client.Get(ctx, types.NamespacedName{Name: volume.Name}, current); err != nil {
		return err
	}
	before := current.DeepCopy()
	updated := make([]storagev1alpha1.DesiredReplica, 0, len(current.Spec.DesiredReplicas))
	for _, replica := range current.Spec.DesiredReplicas {
		if replica.NodeName != a.NodeName || replica.Mode != "cache" {
			updated = append(updated, replica)
		}
	}
	current.Spec.DesiredReplicas = updated
	if err := a.Client.Patch(ctx, current, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("remove expired cached replica: %w", err)
	}
	return nil
}

func replicaForNode(volume *storagev1alpha1.SyncthingVolume, nodeName string) (storagev1alpha1.ReplicaStatus, bool) {
	for _, replica := range volume.Status.Replicas {
		if replica.NodeName == nodeName {
			return replica, true
		}
	}
	return storagev1alpha1.ReplicaStatus{}, false
}

func hasMountedChild(root, volumeID string) bool {
	content, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return true
	}
	marker := "/" + volumeID
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 4 && strings.HasPrefix(unescapeMountInfo(fields[4]), root) && strings.Contains(unescapeMountInfo(fields[4]), marker) {
			return true
		}
	}
	return false
}

func (a *Agent) hasPublishedMount(volumeID string) (bool, error) {
	rootDir := a.MountsDir
	if rootDir == "" {
		rootDir = "/var/lib/csi-syncthing/mounts"
	}
	root := filepath.Join(rootDir, volumeID)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		var mount struct {
			Target string `json:"target"`
		}
		content, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			return false, err
		}
		if err := json.Unmarshal(content, &mount); err != nil {
			return false, err
		}
		if err := validateKubeletPath(mount.Target); err != nil {
			return false, fmt.Errorf("invalid mount reference: %w", err)
		}
		mounted, err := isMountPoint(mount.Target)
		if err != nil {
			return false, err
		}
		if mounted {
			return true, nil
		}
		if err := os.Remove(filepath.Join(root, entry.Name())); err != nil && !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}

func validateKubeletPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.HasPrefix(path, "/var/lib/kubelet/") {
		return fmt.Errorf("path %q is outside the kubelet directory", path)
	}
	return nil
}

func isMountPoint(path string) (bool, error) {
	content, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 4 && unescapeMountInfo(fields[4]) == path {
			return true, nil
		}
	}
	return false, nil
}

func unescapeMountInfo(path string) string {
	path = strings.ReplaceAll(path, `\040`, " ")
	path = strings.ReplaceAll(path, `\011`, "\t")
	path = strings.ReplaceAll(path, `\012`, "\n")
	path = strings.ReplaceAll(path, `\134`, `\`)
	return path
}

func (a *Agent) peerConnected(ctx context.Context, localID string, devices []Device) (bool, error) {
	_ = localID
	if len(devices) == 0 {
		return false, nil
	}
	var response struct {
		Connections map[string]struct {
			Connected bool `json:"connected"`
		} `json:"connections"`
	}
	if err := a.Syncthing.request(ctx, "GET", "/rest/system/connections", nil, &response); err != nil {
		return false, fmt.Errorf("read Syncthing peer connections: %w", err)
	}
	for _, device := range devices {
		connection, ok := response.Connections[device.DeviceID]
		if !ok || !connection.Connected {
			return false, nil
		}
	}
	return true, nil
}

func (a *Agent) publishReplica(ctx context.Context, volume *storagev1alpha1.SyncthingVolume, replica storagev1alpha1.ReplicaStatus) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	current := &storagev1alpha1.SyncthingVolume{}
	if err := a.Client.Get(ctx, types.NamespacedName{Name: volume.Name}, current); err != nil {
		return err
	}
	before := current.DeepCopy()
	found := false
	for i := range current.Status.Replicas {
		if current.Status.Replicas[i].NodeName == a.NodeName {
			if replica.State == "Cached" {
				replica.Completion = current.Status.Replicas[i].Completion
				replica.NeedBytes = current.Status.Replicas[i].NeedBytes
				replica.NeedItems = current.Status.Replicas[i].NeedItems
				replica.Connected = current.Status.Replicas[i].Connected
			}
			if replica.State == "Cached" {
				replica.Completion = current.Status.Replicas[i].Completion
				replica.NeedBytes = current.Status.Replicas[i].NeedBytes
				replica.NeedItems = current.Status.Replicas[i].NeedItems
				replica.Connected = current.Status.Replicas[i].Connected
			}
			if current.Status.Replicas[i].State == replica.State && current.Status.Replicas[i].LastTransitionTime != nil {
				replica.LastTransitionTime = current.Status.Replicas[i].LastTransitionTime
			}
			current.Status.Replicas[i] = replica
			found = true
			break
		}
	}
	if !volume.DeletionTimestamp.IsZero() && replica.State == "Absent" {
		remaining := current.Status.Replicas[:0]
		for _, status := range current.Status.Replicas {
			if status.NodeName != a.NodeName {
				remaining = append(remaining, status)
			}
		}
		current.Status.Replicas = remaining
	}
	if !found {
		current.Status.Replicas = append(current.Status.Replicas, replica)
	}
	if !found || replica.LastTransitionTime == nil {
		now := metav1.Now()
		replica.LastTransitionTime = &now
		if !found {
			current.Status.Replicas[len(current.Status.Replicas)-1].LastTransitionTime = &now
		} else {
			for i := range current.Status.Replicas {
				if current.Status.Replicas[i].NodeName == a.NodeName {
					current.Status.Replicas[i].LastTransitionTime = &now
				}
			}
		}
	}
	for i := range current.Status.Replicas {
		if current.Status.Replicas[i].NodeName == a.NodeName && current.Status.Replicas[i].State == "Absent" {
			current.Status.Replicas = append(current.Status.Replicas[:i], current.Status.Replicas[i+1:]...)
			break
		}
	}
	if err := a.Client.Status().Patch(ctx, current, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("publish replica status: %w", err)
	}
	return nil
}

func NewScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = storagev1alpha1.AddToScheme(scheme)
	return scheme
}

func AddToScheme(scheme *runtime.Scheme) error {
	return storagev1alpha1.AddToScheme(scheme)
}
