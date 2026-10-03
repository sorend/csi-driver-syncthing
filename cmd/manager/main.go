package main

import (
	"flag"
	"os"
	"time"

	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	csiapi "github.com/container-storage-interface/spec/lib/go/csi"
	storagev1alpha1 "github.com/sorend/csi-driver-syncthing/api/v1alpha1"
	"github.com/sorend/csi-driver-syncthing/internal/controller"
	csiserver "github.com/sorend/csi-driver-syncthing/internal/csi"
)

func main() {
	var metricsAddr, probeAddr, controllerEndpoint string
	var leaderElection bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&leaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	defaultEndpoint := "unix:///csi/csi.sock"
	if endpoint := os.Getenv("CSI_ENDPOINT"); endpoint != "" {
		if endpoint[0] == '/' {
			defaultEndpoint = "unix://" + endpoint
		} else {
			defaultEndpoint = endpoint
		}
	}
	flag.StringVar(&controllerEndpoint, "csi-endpoint", defaultEndpoint, "The CSI controller gRPC socket endpoint.")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(storagev1alpha1.AddToScheme(scheme))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElection,
		LeaderElectionID:       "csi-driver-syncthing.syncthing-storage.sorend.github.com",
	})
	if err != nil {
		os.Exit(1)
	}

	if err := (&controller.SyncthingVolumeReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}).SetupWithManager(mgr); err != nil {
		os.Exit(1)
	}
	if err := (&controller.SyncthingNodeReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		os.Exit(1)
	}
	signalContext := ctrl.SetupSignalHandler()
	go func() {
		if err := csiserver.Serve(signalContext, controllerEndpoint, func(server *grpc.Server) {
			csiapi.RegisterControllerServer(server, &csiserver.Controller{Client: mgr.GetClient(), Poll: 2 * time.Second})
		}); err != nil {
			if signalContext.Err() != nil {
				return
			}
			os.Exit(1)
		}
	}()
	if err := mgr.Start(signalContext); err != nil {
		os.Exit(1)
	}
}
