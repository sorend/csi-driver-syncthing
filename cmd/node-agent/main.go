package main

import (
	"context"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sorend/csi-driver-syncthing/internal/agent"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func main() {
	nodeName := flag.String("node-name", os.Getenv("NODE_NAME"), "Kubernetes node name")
	nodeIP := flag.String("node-ip", os.Getenv("NODE_IP"), "Kubernetes node IP address")
	apiURL := flag.String("syncthing-url", "http://127.0.0.1:8384", "Local Syncthing REST API URL")
	apiKey := flag.String("syncthing-api-key", os.Getenv("ST_API_KEY"), "Syncthing REST API key")
	configPath := flag.String("syncthing-config", "/var/syncthing/config/config.xml", "Syncthing config.xml path used to read the local API key")
	volumesDir := flag.String("volumes-dir", "/var/lib/csi-syncthing/volumes", "Local volume data directory")
	mountsDirFlag := flag.String("mounts-dir", "/var/lib/csi-syncthing/mounts", "Node-local mount reference directory")
	flag.Parse()
	mountsDir := *mountsDirFlag
	if value := os.Getenv("MOUNTS_DIR"); value != "" {
		mountsDir = value
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *nodeName == "" || *nodeIP == "" {
		panic("NODE_NAME and NODE_IP are required")
	}
	if *apiKey == "" {
		var err error
		*apiKey, err = waitForAPIKey(ctx, *configPath)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			panic(err)
		}
	}
	if *apiKey == "" {
		panic("NODE_NAME, NODE_IP, and Syncthing API key are required")
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := agent.AddToScheme(scheme); err != nil {
		panic(err)
	}
	k8s, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}
	runner := &agent.Agent{
		Client:    k8s,
		Syncthing: &agent.Syncthing{BaseURL: *apiURL, APIKey: *apiKey},
		NodeName:  *nodeName, NodeIP: *nodeIP, VolumesDir: *volumesDir, MountsDir: mountsDir, Interval: 10 * time.Second,
	}
	if err := runner.Run(ctx); err != nil {
		panic(err)
	}
}

func waitForAPIKey(ctx context.Context, path string) (string, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		apiKey, err := readAPIKey(path)
		if err == nil {
			return apiKey, nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("wait for Syncthing API key: %w (last error: %v)", ctx.Err(), err)
		case <-ticker.C:
		}
	}
}

func readAPIKey(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open Syncthing config %q: %w", path, err)
	}
	defer file.Close()
	var config struct {
		GUI struct {
			APIKey string `xml:"apikey"`
		} `xml:"gui"`
	}
	decoder := xml.NewDecoder(file)
	if err := decoder.Decode(&config); err != nil && err != io.EOF {
		return "", fmt.Errorf("decode Syncthing config: %w", err)
	}
	if config.GUI.APIKey == "" {
		return "", fmt.Errorf("Syncthing config has no GUI API key")
	}
	return config.GUI.APIKey, nil
}
