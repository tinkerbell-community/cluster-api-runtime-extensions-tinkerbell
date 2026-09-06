// The talos-upgrade-coordinator manager performs the cluster-scoped remainder of
// `talosctl upgrade-k8s`: bootstrap-manifest SSA sync into the workload cluster,
// gated on cluster-wide version convergence (including the terraform bootstrap
// node CAPI cannot see). See docs/talos-upgrade-coordinator.md.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/tinkerbell-community/tinkerbell-bmc-discovery-controller/internal/logging"
	"github.com/tinkerbell-community/tinkerbell-bmc-discovery-controller/internal/upgrade"
)

// Build metadata injected by goreleaser via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	builtBy = "unknown"
)

type options struct {
	watchNamespace      string
	gateRequeueInterval time.Duration
	gateIgnoreNodes     string
	reconcileTimeout    time.Duration
	resyncPeriod        time.Duration
	dryRun              bool
	manifestsForce      bool
	manifestsNoPrune    bool
	leaderElect         bool
	metricsAddr         string
	probeAddr           string
	logLevel            string
	logFormat           string
}

func registerFlags(o *options) {
	flag.StringVar(&o.watchNamespace, "watch-namespace", "", "Namespace to watch; empty watches all namespaces.")
	flag.DurationVar(&o.gateRequeueInterval, "gate-requeue-interval", time.Minute,
		"Requeue interval while the convergence gate is blocked (workload Nodes cannot be watched).")
	flag.StringVar(&o.gateIgnoreNodes, "gate-ignore-nodes", "",
		"Comma-separated workload Node names skipped by the kubelet-version gate (escape hatch for dead Node objects).")
	flag.DurationVar(&o.reconcileTimeout, "reconcile-timeout", 5*time.Minute,
		"Bound on the post-apply wait for synced manifests to reconcile.")
	flag.DurationVar(&o.resyncPeriod, "resync-period", 0,
		"Re-run the sync at the current pair to repair drift; 0 disables (recommended default).")
	flag.BoolVar(&o.dryRun, "dry-run", false, "Diff and report without writing to the workload cluster.")
	flag.BoolVar(&o.manifestsForce, "manifests-force", false, "Recreate objects with immutable field changes.")
	flag.BoolVar(&o.manifestsNoPrune, "manifests-no-prune", false, "Disable pruning of objects Talos no longer renders.")
	flag.BoolVar(&o.leaderElect, "leader-elect", true, "Enable leader election.")
	flag.StringVar(&o.metricsAddr, "metrics-bind-address", ":8080", "Metrics endpoint bind address.")
	flag.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "Health probe bind address.")
	flag.StringVar(&o.logLevel, "log-level", "info", "Log level: debug, info, warn, or error.")
	flag.StringVar(&o.logFormat, "log-format", "json", "Log format: json or text.")
}

func main() {
	var opts options
	registerFlags(&opts)
	flag.Parse()

	root, err := logging.New(opts.logLevel, opts.logFormat, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctrl.SetLogger(logr.FromSlogHandler(root.Handler()))
	log := logging.Component(root, "setup")
	log.Info(upgrade.Name, "version", version, "commit", commit, "date", date, "builtBy", builtBy)

	if err := run(opts, log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run(opts options, log *slog.Logger) error {
	mgr, err := buildManager(opts)
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	ignore := sets.New[string]()
	for _, n := range strings.Split(opts.gateIgnoreNodes, ",") {
		if n = strings.TrimSpace(n); n != "" {
			ignore.Insert(n)
		}
	}

	reconciler := &upgrade.Reconciler{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorder(upgrade.Name),
		NewWorkload: func(kubeconfig []byte) (kubernetes.Interface, *rest.Config, error) {
			cfg, err := upgrade.WorkloadRESTConfig(kubeconfig)
			if err != nil {
				return nil, nil, err
			}
			clientset, err := kubernetes.NewForConfig(cfg)
			if err != nil {
				return nil, nil, fmt.Errorf("building workload clientset: %w", err)
			}
			return clientset, cfg, nil
		},
		NewTalos: upgrade.NewTalosConn,
		Sync:     upgrade.SyncBootstrapManifests,
		Opts: upgrade.Options{
			GateRequeueInterval: opts.gateRequeueInterval,
			ReconcileTimeout:    opts.reconcileTimeout,
			ResyncPeriod:        opts.resyncPeriod,
			IgnoreNodes:         ignore,
			DryRun:              opts.dryRun,
			ManifestsForce:      opts.manifestsForce,
			ManifestsNoPrune:    opts.manifestsNoPrune,
		},
	}
	if err := reconciler.SetupWithManager(mgr, 1); err != nil {
		return fmt.Errorf("setting up controller: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("setting up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("setting up ready check: %w", err)
	}

	log.Info("starting manager", "dryRun", opts.dryRun)
	return mgr.Start(ctrl.SetupSignalHandler())
}

func buildManager(opts options) (ctrl.Manager, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, clusterv1.AddToScheme} {
		if err := add(scheme); err != nil {
			return nil, fmt.Errorf("building scheme: %w", err)
		}
	}

	cacheOptions := cache.Options{}
	if opts.watchNamespace != "" {
		cacheOptions.DefaultNamespaces = map[string]cache.Config{opts.watchNamespace: {}}
	}

	return ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: opts.metricsAddr},
		HealthProbeBindAddress: opts.probeAddr,
		LeaderElection:         opts.leaderElect,
		LeaderElectionID:       "talos-upgrade-coordinator.upgrade.tinkerbell.org",
		Cache:                  cacheOptions,
	})
}
