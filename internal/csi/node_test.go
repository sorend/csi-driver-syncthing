package csi

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if err := validateTargetVolume("/var/lib/kubelet/pods/pod/volumes/kubernetes.io~csi/pvc-1/mount", "pvc-1"); err != nil {
		t.Fatalf("valid kubelet CSI target rejected: %v", err)
	}
	if err := validateTargetVolume("/var/lib/kubelet/plugins/csi.syncthing.io/stage/pvc-1/staging_target", "pvc-1"); err != nil {
		t.Fatalf("valid stage path rejected: %v", err)
	}
	if err := validateTargetVolume("/var/lib/kubelet/pods/pod/volumes/kubernetes.io~csi/pvc-2/mount", "pvc-1"); err == nil {
		t.Fatal("target for another volume accepted")
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
