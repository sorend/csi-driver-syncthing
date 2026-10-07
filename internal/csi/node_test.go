package csi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
)

func TestServeDoesNotChangeSocketDirectoryPermissions(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0751); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "csi.sock")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, "unix://"+socket, func(*grpc.Server) {})
	}()

	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("Serve did not create the CSI socket")
		}
		time.Sleep(time.Millisecond)
	}

	info, err := os.Stat(root)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0751 {
		cancel()
		t.Fatalf("socket directory permissions = %#o, want %#o", got, 0751)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned an error after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop after cancellation")
	}
}

func TestServeKeepsSocketOfReplacementServer(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "csi.sock")
	endpoint := "unix://" + socket

	cancelFirst, first, firstServing := serveAsync(t, endpoint)
	waitForServe(t, firstServing, first)
	firstInfo, err := os.Lstat(socket)
	if err != nil {
		cancelFirst()
		t.Fatal(err)
	}

	// A replacement server binds the same path before the first one is
	// terminated, exactly as a rolling restart of the controller does.
	cancelSecond, second, secondServing := serveAsync(t, endpoint)
	waitForServe(t, secondServing, second)
	secondInfo, err := os.Lstat(socket)
	if err != nil {
		cancelFirst()
		cancelSecond()
		t.Fatal(err)
	}
	if os.SameFile(firstInfo, secondInfo) {
		cancelFirst()
		cancelSecond()
		t.Fatal("replacement server did not take over the socket path")
	}

	cancelFirst()
	waitForServeStop(t, first)
	current, err := os.Lstat(socket)
	if err != nil {
		cancelSecond()
		t.Fatalf("socket of the replacement server removed by the stopped server: %v", err)
	}
	if !os.SameFile(secondInfo, current) {
		cancelSecond()
		t.Fatal("socket of the replacement server was replaced on shutdown")
	}

	cancelSecond()
	waitForServeStop(t, second)
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket of the last server left behind: %v", err)
	}
}

// serveAsync runs Serve in the background and reports through serving once the
// socket is bound and through done once Serve returned.
func serveAsync(t *testing.T, endpoint string) (context.CancelFunc, <-chan error, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	serving := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, endpoint, func(*grpc.Server) { close(serving) })
	}()
	return cancel, done, serving
}

func waitForServe(t *testing.T, serving <-chan struct{}, done <-chan error) {
	t.Helper()
	select {
	case <-serving:
	case err := <-done:
		t.Fatalf("Serve returned before binding the socket: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not bind the socket")
	}
}

func waitForServeStop(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned an error after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop after cancellation")
	}
}

func TestValidateNodePath(t *testing.T) {
	for _, test := range []struct {
		path string
		want bool
	}{
		{path: "/var/lib/kubelet/pods/pod/volume", want: true},
		{path: "/tmp/volume", want: false},
		{path: "/var/lib/kubelet/../etc", want: false},
		{path: "relative", want: false},
	} {
		t.Run(test.path, func(t *testing.T) {
			err := validateNodePath(test.path)
			if (err == nil) != test.want {
				t.Fatalf("validateNodePath(%q) error = %v", test.path, err)
			}
		})
	}
}

func TestValidateVolumeIDRequiresDNSLabel(t *testing.T) {
	for _, test := range []struct {
		id   string
		want bool
	}{{"volume-a", true}, {"Volume-A", false}, {"../etc", false}, {"", false}} {
		err := validateVolumeID(test.id)
		if (err == nil) != test.want {
			t.Errorf("validateVolumeID(%q) error = %v", test.id, err)
		}
	}
}

func TestValidateMountFlagsRejectsDangerousFlags(t *testing.T) {
	if err := validateMountFlags([]string{"noatime", "nodev"}); err != nil {
		t.Fatalf("valid flags rejected: %v", err)
	}
	if err := validateMountFlags([]string{"bind"}); err == nil {
		t.Fatal("bind mount flag accepted from volume request")
	}
}

func TestValidateTargetVolume(t *testing.T) {
	if err := validateTargetVolume("/var/lib/kubelet/pods/pod/volumes/kubernetes.io~csi/pvc-1234/mount", "pvc-1-suffix"); err != nil {
		t.Fatalf("valid kubelet CSI target with PV name differing from volume ID rejected: %v", err)
	}
	if err := validateTargetVolume("/var/lib/kubelet/pods/pod/volumes/kubernetes.io~csi/pvc-1234/mount", "pvc-1"); err != nil {
		t.Fatalf("valid target rejected when PV name differs from volume ID: %v", err)
	}
	if err := validateTargetVolume("/var/lib/kubelet/pods/csi-sanity/volumes/kubernetes.io~csi/sanity/target", "sanity"); err != nil {
		t.Fatalf("valid csi-sanity target rejected: %v", err)
	}
	if err := validateTargetVolume("/var/lib/kubelet/pods/pod/volumes/other/pvc-1/mount", "pvc-1"); err == nil {
		t.Fatal("non-CSI kubelet target accepted")
	}
	if err := validateTargetVolume("/var/lib/kubelet/plugins/kubernetes.io/csi/csi.syncthing.io/pvc-1/globalmount", "pvc-1"); err == nil {
		t.Fatal("staging path accepted as a pod target")
	}
}

func TestValidateStagingVolume(t *testing.T) {
	volumeID := "pvc-1"
	volumeHash := sha256.Sum256([]byte(volumeID))
	stagingPath := filepath.Join("/var/lib/kubelet/plugins/kubernetes.io/csi", DriverName, hex.EncodeToString(volumeHash[:]), "globalmount")
	if err := validateStagingVolume(stagingPath, volumeID); err != nil {
		t.Fatalf("valid driver-specific kubelet staging path rejected: %v", err)
	}
	if err := validateStagingVolume(stagingPath, "pvc-2"); err == nil {
		t.Fatal("driver-specific staging path for a different volume accepted")
	}
	if err := validateStagingVolume(filepath.Join("/var/lib/kubelet/plugins/kubernetes.io/csi", DriverName, "not-a-volume-hash", "globalmount"), volumeID); err == nil {
		t.Fatal("driver-specific staging path with invalid volume hash accepted")
	}
	if err := validateStagingVolume("/var/lib/kubelet/plugins/kubernetes.io/csi/pv/pvc-1234/globalmount", "pvc-1-suffix"); err != nil {
		t.Fatalf("valid kubelet staging path with PV name differing from volume ID rejected: %v", err)
	}
	if err := validateStagingVolume("/var/lib/kubelet/plugins/kubernetes.io/csi/pv/pvc-1/globalmount", "pvc-1"); err != nil {
		t.Fatalf("valid kubelet staging path rejected: %v", err)
	}
	if err := validateStagingVolume("/var/lib/kubelet/plugins/kubernetes.io/csi/csi.syncthing.io/pv/pvc-1/globalmount", "pvc-1"); err != nil {
		t.Fatalf("valid driver-scoped kubelet staging path rejected: %v", err)
	}
	if err := validateStagingVolume("/var/lib/kubelet/plugins/kubernetes.io/csi/pv/pvc-1/not-globalmount", "pvc-1"); err == nil {
		t.Fatal("invalid kubelet staging target accepted")
	}
	if err := validateStagingVolume("/var/lib/kubelet/plugins/kubernetes.io/csi/pv/pvc-1/nested/globalmount", "pvc-1"); err == nil {
		t.Fatal("nested staging target accepted")
	}
	if err := validateStagingVolume("/var/lib/kubelet/plugins/kubernetes.io/csi/other.csi.io/pv/pvc-1/globalmount", "pvc-1"); err == nil {
		t.Fatal("staging target for a different CSI driver accepted")
	}
}

func TestMountMarkerIsStableAndVolumeScoped(t *testing.T) {
	root := t.TempDir()
	node := &Node{MountsDir: root}
	first, err := node.mountMarker("volume-a", "/var/lib/kubelet/pods/pod-a/volume")
	if err != nil {
		t.Fatal(err)
	}
	second, err := node.mountMarker("volume-a", "/var/lib/kubelet/pods/pod-a/volume")
	if err != nil {
		t.Fatal(err)
	}
	third, err := node.mountMarker("volume-b", "/var/lib/kubelet/pods/pod-a/volume")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == third {
		t.Fatalf("marker paths are not stable and volume-scoped: %q %q %q", first, second, third)
	}
	if _, err := os.Stat(filepath.Dir(first)); err != nil {
		t.Fatal(err)
	}
}
