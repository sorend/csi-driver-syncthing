package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	csiapi "github.com/container-storage-interface/spec/lib/go/csi"
	csiserver "github.com/sorend/csi-driver-syncthing/internal/csi"
	"google.golang.org/grpc"
)

func main() {
	endpoint := flag.String("csi-endpoint", "unix:///csi/csi.sock", "CSI gRPC socket endpoint")
	nodeID := flag.String("node-id", os.Getenv("NODE_NAME"), "Kubernetes node name")
	volumesDir := flag.String("volumes-dir", envOr("VOLUMES_DIR", "/var/lib/csi-syncthing/volumes"), "Local Syncthing data directory")
	mountsDir := flag.String("mounts-dir", "/var/lib/csi-syncthing/mounts", "Node-local publish reference directory")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := csiserver.Serve(ctx, *endpoint, func(server *grpc.Server) {
		csiapi.RegisterNodeServer(server, &csiserver.Node{NodeID: *nodeID, VolumesDir: *volumesDir, MountsDir: *mountsDir})
	}); err != nil {
		panic(err)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
