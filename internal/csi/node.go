package csi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Node struct {
	csi.UnimplementedNodeServer
	NodeID     string
	VolumesDir string
	MountsDir  string
}

func (s *Node) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	if s.NodeID == "" {
		return nil, status.Error(codes.FailedPrecondition, "Kubernetes node name is not configured")
	}
	return &csi.NodeGetInfoResponse{NodeId: s.NodeID}, nil
}

func (s *Node) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{Capabilities: []*csi.NodeServiceCapability{
		{Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME}}},
	}}, nil
}

func (s *Node) NodeStageVolume(_ context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	if req.GetVolumeId() == "" || req.GetStagingTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID and staging path are required")
	}
	if err := validateVolumeID(req.GetVolumeId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := validateNodePath(req.GetStagingTargetPath()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid staging path: %v", err)
	}
	if err := validateStagingVolume(req.GetStagingTargetPath(), req.GetVolumeId()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid staging path for volume: %v", err)
	}
	if err := validateStagingVolume(req.GetStagingTargetPath(), req.GetVolumeId()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid staging path for volume: %v", err)
	}
	if mounted, err := isMountPoint(req.GetStagingTargetPath()); err != nil {
		return nil, status.Errorf(codes.Internal, "inspect staging path: %v", err)
	} else if mounted {
		expectedSource := filepath.Join(s.VolumesDir, req.GetVolumeId())
		if mountSource(req.GetStagingTargetPath()) != expectedSource && !hasPathPrefix(mountSource(req.GetStagingTargetPath()), expectedSource) {
			return nil, status.Error(codes.AlreadyExists, "staging path is mounted from a different volume")
		}
		return &csi.NodeStageVolumeResponse{}, nil
	}
	if req.GetVolumeCapability() == nil || req.GetVolumeCapability().GetMount() == nil {
		return nil, status.Error(codes.InvalidArgument, "only filesystem mount volumes are supported")
	}
	if req.GetVolumeCapability().GetMount().GetFsType() != "" && req.GetVolumeCapability().GetMount().GetFsType() != "ext4" {
		return nil, status.Error(codes.InvalidArgument, "only ext4 filesystem type is supported")
	}
	if err := validateMountFlags(req.GetVolumeCapability().GetMount().GetMountFlags()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	volumesDir := s.VolumesDir
	if volumesDir == "" {
		volumesDir = "/var/lib/csi-syncthing/volumes"
	}
	s.VolumesDir = volumesDir
	if !filepath.IsAbs(volumesDir) || filepath.Clean(volumesDir) != volumesDir {
		return nil, status.Error(codes.FailedPrecondition, "volume root must be a clean absolute path")
	}
	if !strings.HasPrefix(volumesDir, "/var/lib/csi-syncthing/") {
		return nil, status.Error(codes.FailedPrecondition, "volume root must be under /var/lib/csi-syncthing")
	}
	source := filepath.Join(volumesDir, req.GetVolumeId())
	info, err := os.Stat(source)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "local replica is not ready: %v", err)
	}
	if !info.IsDir() {
		return nil, status.Error(codes.FailedPrecondition, "local replica path is not a directory")
	}
	if mode := req.GetVolumeCapability().GetAccessMode().GetMode(); mode != csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER {
		return nil, status.Error(codes.InvalidArgument, "only single-node writer access mode is supported")
	}
	if err := os.MkdirAll(filepath.Dir(req.GetStagingTargetPath()), 0750); err != nil {
		return nil, status.Errorf(codes.Internal, "create staging parent: %v", err)
	}
	if err := os.Mkdir(req.GetStagingTargetPath(), 0750); err != nil && !os.IsExist(err) {
		return nil, status.Errorf(codes.Internal, "create staging path: %v", err)
	}
	if err := mountIfNeeded(source, req.GetStagingTargetPath(), false); err != nil {
		return nil, status.Errorf(codes.Internal, "stage volume: %v", err)
	}
	return &csi.NodeStageVolumeResponse{}, nil
}

func (s *Node) NodeUnstageVolume(_ context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	if req.GetVolumeId() == "" || req.GetStagingTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID and staging path are required")
	}
	if err := validateNodePath(req.GetStagingTargetPath()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid staging path: %v", err)
	}
	if err := validateVolumeID(req.GetVolumeId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := validateStagingVolume(req.GetStagingTargetPath(), req.GetVolumeId()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid staging path for volume: %v", err)
	}
	if err := unmountIfNeeded(req.GetStagingTargetPath()); err != nil {
		return nil, status.Errorf(codes.Internal, "unstage volume: %v", err)
	}
	if err := os.Remove(req.GetStagingTargetPath()); err != nil && !os.IsNotExist(err) {
		return nil, status.Errorf(codes.Internal, "remove staging target: %v", err)
	}
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (s *Node) NodePublishVolume(_ context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	if req.GetVolumeId() == "" || req.GetStagingTargetPath() == "" || req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID, staging path, and target path are required")
	}
	if err := validateVolumeID(req.GetVolumeId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if req.GetVolumeCapability() == nil || req.GetVolumeCapability().GetMount() == nil {
		return nil, status.Error(codes.InvalidArgument, "only filesystem mount volumes are supported")
	}
	if req.GetVolumeCapability().GetAccessMode().GetMode() != csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER {
		return nil, status.Error(codes.InvalidArgument, "only single-node writer access mode is supported")
	}
	if err := validateMountFlags(req.GetVolumeCapability().GetMount().GetMountFlags()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := validateNodePath(req.GetStagingTargetPath()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid staging path: %v", err)
	}
	if err := validateNodePath(req.GetTargetPath()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid target path: %v", err)
	}
	if err := validateTargetVolume(req.GetTargetPath(), req.GetVolumeId()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid target path for volume: %v", err)
	}
	if mounted, err := isMountPoint(req.GetTargetPath()); err != nil {
		return nil, status.Errorf(codes.Internal, "inspect publish target: %v", err)
	} else if mounted {
		source := mountSource(req.GetTargetPath())
		stagingSource := mountSource(req.GetStagingTargetPath())
		if source == "" || stagingSource == "" || source != stagingSource {
			return nil, status.Error(codes.AlreadyExists, "publish target is mounted from a different source")
		}
		marker, err := s.mountMarker(req.GetVolumeId(), req.GetTargetPath())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "create existing mount marker: %v", err)
		}
		content, _ := json.Marshal(struct {
			Target   string `json:"target"`
			ReadOnly bool   `json:"readOnly"`
		}{Target: req.GetTargetPath(), ReadOnly: req.GetReadonly() || hasMountFlag(req.GetVolumeCapability().GetMount().GetMountFlags(), "ro")})
		if err := os.WriteFile(marker, content, 0644); err != nil {
			return nil, status.Errorf(codes.Internal, "write existing mount marker: %v", err)
		}
		return &csi.NodePublishVolumeResponse{}, nil
	}
	if err := os.MkdirAll(filepath.Dir(req.GetTargetPath()), 0750); err != nil {
		return nil, status.Errorf(codes.Internal, "create publish target: %v", err)
	}
	if info, err := os.Stat(req.GetTargetPath()); err == nil {
		sourceInfo, sourceErr := os.Stat(req.GetStagingTargetPath())
		if sourceErr != nil || info.IsDir() != sourceInfo.IsDir() {
			return nil, status.Error(codes.FailedPrecondition, "publish target type does not match the staged volume")
		}
	} else if os.IsNotExist(err) {
		fileInfo, statErr := os.Stat(req.GetStagingTargetPath())
		if statErr != nil {
			return nil, status.Errorf(codes.Internal, "stat staging path: %v", statErr)
		}
		if fileInfo.IsDir() {
			if err := os.Mkdir(req.GetTargetPath(), 0750); err != nil && !os.IsExist(err) {
				return nil, status.Errorf(codes.Internal, "create directory target: %v", err)
			}
		} else if file, err := os.OpenFile(req.GetTargetPath(), os.O_CREATE|os.O_WRONLY, 0600); err != nil {
			return nil, status.Errorf(codes.Internal, "create file target: %v", err)
		} else {
			_ = file.Close()
		}
	} else {
		return nil, status.Errorf(codes.Internal, "stat publish target: %v", err)
	}
	marker, err := s.mountMarker(req.GetVolumeId(), req.GetTargetPath())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create mount marker: %v", err)
	}
	readOnly := req.GetReadonly()
	content, _ := json.Marshal(struct {
		Target   string `json:"target"`
		ReadOnly bool   `json:"readOnly"`
	}{Target: req.GetTargetPath(), ReadOnly: readOnly})
	if err := os.WriteFile(marker, content, 0666); err != nil {
		return nil, status.Errorf(codes.Internal, "write mount marker: %v", err)
	}
	if err := mountIfNeeded(req.GetStagingTargetPath(), req.GetTargetPath(), readOnly); err != nil {
		_ = os.Remove(marker)
		return nil, status.Errorf(codes.Internal, "publish volume: %v", err)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

func (s *Node) NodeUnpublishVolume(_ context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	if req.GetVolumeId() == "" || req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID and target path are required")
	}
	if err := validateNodePath(req.GetTargetPath()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid target path: %v", err)
	}
	if err := validateVolumeID(req.GetVolumeId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if _, err := mountMarkerPath(s.MountsDir, req.GetVolumeId(), req.GetTargetPath()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid target path: %v", err)
	}
	if err := unmountIfNeeded(req.GetTargetPath()); err != nil {
		return nil, status.Errorf(codes.Internal, "unpublish volume: %v", err)
	}
	if req.GetVolumeId() != "" {
		if err := validateVolumeID(req.GetVolumeId()); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		marker, err := s.mountMarker(req.GetVolumeId(), req.GetTargetPath())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "resolve mount marker: %v", err)
		}
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return nil, status.Errorf(codes.Internal, "remove mount marker: %v", err)
		}
		if err := os.Remove(req.GetTargetPath()); err != nil && !os.IsNotExist(err) {
			return nil, status.Errorf(codes.Internal, "remove publish target: %v", err)
		}
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (*Node) NodeGetVolumeStats(context.Context, *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "volume statistics are not implemented")
}

func mountIfNeeded(source, target string, readOnly bool) error {
	if mounted, err := isMountPoint(target); err != nil {
		return err
	} else if mounted {
		if readOnly {
			return syscall.Mount("", target, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY, "")
		}
		return nil
	}
	if err := syscall.Mount(source, target, "", syscall.MS_BIND, ""); err != nil {
		return err
	}
	if err := unix.Mount("", target, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		_ = syscall.Unmount(target, 0)
		return err
	}
	if readOnly {
		if err := syscall.Mount("", target, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY, ""); err != nil {
			_ = syscall.Unmount(target, 0)
			return err
		}
	}
	return nil
}

func unmountIfNeeded(target string) error {
	mounted, err := isMountPoint(target)
	if err != nil || !mounted {
		return err
	}
	if err := syscall.Unmount(target, 0); err != nil && err != syscall.EINVAL && err != syscall.ENOENT {
		return err
	}
	return nil
}

func isMountPoint(target string) (bool, error) {
	content, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 4 && unescapeMountInfo(fields[4]) == target {
			return true, nil
		}
	}
	return false, nil
}

func mountSource(target string) string {
	content, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || unescapeMountInfo(fields[4]) != target {
			continue
		}
		for i := 6; i+2 < len(fields); i++ {
			if fields[i] == "-" {
				return fields[i+2]
			}
		}
	}
	return ""
}

func hasPathPrefix(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

func mountedFrom(target, source string) bool {
	content, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || unescapeMountInfo(fields[4]) != target {
			continue
		}
		for i := 6; i < len(fields)-2; i++ {
			if fields[i] == "-" && i+2 < len(fields) {
				return fields[i+2] == source
			}
		}
	}
	return false
}

func unescapeMountInfo(path string) string {
	path = strings.ReplaceAll(path, `\040`, " ")
	path = strings.ReplaceAll(path, `\011`, "\t")
	path = strings.ReplaceAll(path, `\012`, "\n")
	path = strings.ReplaceAll(path, `\134`, `\`)
	return path
}

func validateVolumeID(id string) error {
	if id == "" || id == "." || id == ".." || len(id) > 63 || !dnsLabelPattern.MatchString(id) || strings.ContainsAny(id, "/\\") || filepath.Base(id) != id {
		return fmt.Errorf("invalid volume ID %q", id)
	}
	return nil
}

func validateNodePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return fmt.Errorf("path must be a clean absolute path beneath the kubelet directory")
	}
	if !strings.HasPrefix(path, "/var/lib/kubelet/") {
		return fmt.Errorf("path is outside /var/lib/kubelet")
	}
	return nil
}

func validateTargetVolume(path, volumeID string) error {
	path = filepath.Clean(path)
	isPodTarget := strings.Contains(path, "/volumes/kubernetes.io~csi/")
	isStagePath := strings.Contains(path, "/plugins/kubernetes.io/csi/csi.syncthing.io/") || strings.Contains(path, "/plugins/csi.syncthing.io/")
	if !isPodTarget && !isStagePath {
		return fmt.Errorf("path is not under the Syncthing CSI plugin directory")
	}
	if !strings.Contains(path, "/"+volumeID+"/") {
		return fmt.Errorf("path does not contain the requested volume ID")
	}
	return nil
}

func validateStagingVolume(path, volumeID string) error {
	if !strings.HasPrefix(filepath.Clean(path), "/var/lib/kubelet/plugins/kubernetes.io/csi/csi.syncthing.io/") {
		return fmt.Errorf("path is outside the CSI staging directory")
	}
	if !strings.Contains(path, "/"+volumeID+"/") {
		return fmt.Errorf("path does not contain the volume ID")
	}
	return nil
}

func validateMountFlags(flags []string) error {
	for _, flag := range flags {
		switch flag {
		case "rw", "noatime", "nodev", "nosuid", "relatime":
		default:
			return fmt.Errorf("unsupported mount flag %q", flag)
		}
	}
	return nil
}

func hasMountFlag(flags []string, desired string) bool {
	for _, flag := range flags {
		if flag == desired {
			return true
		}
	}
	return false
}

func (s *Node) mountMarker(volumeID, target string) (string, error) {
	return mountMarkerPath(s.MountsDir, volumeID, target)
}

func mountMarkerPath(root, volumeID, target string) (string, error) {
	if err := validateVolumeID(volumeID); err != nil {
		return "", err
	}
	if root == "" {
		root = "/var/lib/csi-syncthing/mounts"
	}
	digest := sha256.Sum256([]byte(target))
	dir := filepath.Join(root, volumeID)
	if err := os.MkdirAll(dir, 0777); err != nil {
		return "", err
	}
	return filepath.Join(dir, hex.EncodeToString(digest[:])), nil
}
