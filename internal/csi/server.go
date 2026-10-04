package csi

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const DriverName = "csi.syncthing.io"

func Serve(ctx context.Context, endpoint string, register func(*grpc.Server)) error {
	if endpoint == "" {
		return fmt.Errorf("CSI endpoint is required")
	}
	socket := endpoint
	if strings.HasPrefix(endpoint, "unix://") {
		socket = strings.TrimPrefix(endpoint, "unix://")
	} else if strings.HasPrefix(endpoint, "unix:") {
		socket = strings.TrimPrefix(endpoint, "unix:")
	} else {
		return fmt.Errorf("unsupported CSI endpoint %q: only unix sockets are supported", endpoint)
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0750); err != nil {
		return fmt.Errorf("create CSI socket directory: %w", err)
	}
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale CSI socket: %w", err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("listen on CSI socket: %w", err)
	}
	defer os.Remove(socket)
	if err := os.Chmod(socket, 0660); err != nil {
		_ = listener.Close()
		return fmt.Errorf("set CSI socket permissions: %w", err)
	}
	server := grpc.NewServer()
	csi.RegisterIdentityServer(server, &identityServer{})
	register(server)
	reflection.Register(server)
	go func() {
		<-ctx.Done()
		server.GracefulStop()
	}()
	if err := server.Serve(listener); err != nil {
		return fmt.Errorf("serve CSI gRPC: %w", err)
	}
	return nil
}

type identityServer struct {
	csi.UnimplementedIdentityServer
}

func (*identityServer) GetPluginInfo(context.Context, *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{Name: DriverName, VendorVersion: "0.1.0"}, nil
}

func (*identityServer) GetPluginCapabilities(context.Context, *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{Capabilities: []*csi.PluginCapability{{
		Type: &csi.PluginCapability_Service_{Service: &csi.PluginCapability_Service{Type: csi.PluginCapability_Service_CONTROLLER_SERVICE}},
	}}}, nil
}

func (*identityServer) Probe(context.Context, *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{Ready: wrapperspb.Bool(true)}, nil
}
