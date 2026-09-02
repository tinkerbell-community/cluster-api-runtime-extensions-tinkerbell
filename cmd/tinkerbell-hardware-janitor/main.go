// The tinkerbell-hardware-janitor manager restores released Tinkerbell
// Hardware to a clean baseline: it clears the secret-bearing spec.userData
// (the full Talos machineconfig tootles keeps serving), clears stale OS
// metadata, and parks the netboot posture. See
// docs/tinkerbell-hardware-janitor.md.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/tinkerbell-community/tinkerbell-bmc-discovery-controller/internal/janitor"
	"github.com/tinkerbell-community/tinkerbell-bmc-discovery-controller/internal/logging"
)

// Build metadata injected by goreleaser via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	builtBy = "unknown"
)

func main() {
	var (
		namespace       string
		clearOSMetadata bool
		baselineAllow   bool
		resyncPeriod    time.Duration
		leaderElect     bool
		metricsAddr     string
		probeAddr       string
		logLevel        string
		logFormat       string
	)
	flag.StringVar(&namespace, "namespace", "tinkerbell", "Namespace watched for Hardware.")
	flag.BoolVar(&clearOSMetadata, "clear-os-metadata", true, "Clear metadata.instance.operating_system and .state on scrub (the talos-os-metadata handoff); disable in environments still on terraform-written OS metadata.")
	flag.BoolVar(&baselineAllow, "baseline-allow-pxe", false, "Parked allowPXE value asserted on released hardware's netboot-bearing interfaces.")
	flag.DurationVar(&resyncPeriod, "resync-period", time.Hour, "Informer resync; bounds the catch-up window for missed events.")
	flag.BoolVar(&leaderElect, "leader-elect", true, "Enable leader election.")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Metrics endpoint bind address.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Health probe bind address.")
	flag.StringVar(&logLevel, "log-level", "info", "Log level: debug, info, warn, or error.")
	flag.StringVar(&logFormat, "log-format", "json", "Log format: json or text.")
	flag.Parse()

	root, err := logging.New(logLevel, logFormat, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctrl.SetLogger(logr.FromSlogHandler(root.Handler()))
	log := logging.Component(root, "setup")
	log.Info(janitor.Name, "version", version, "commit", commit, "date", date, "builtBy", builtBy)

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, tinkv1.AddToScheme} {
		if err := add(scheme); err != nil {
			log.Error("unable to build scheme", "err", err)
			os.Exit(1)
		}
	}

	// The cache is filtered server-side to owner-label-absent Hardware: the
	// apiserver synthesizes an Add when a release removes the label and a
	// Delete when a claim adds it, so the janitor's informer never holds a
	// claimed object's secret-bearing spec in memory at all.
	selector, err := labels.Parse("!" + janitor.OwnerNameLabel)
	if err != nil {
		log.Error("unable to parse hardware label selector", "err", err)
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          leaderElect,
		LeaderElectionID:        "tinkerbell-hardware-janitor.janitor.tinkerbell.org",
		LeaderElectionNamespace: namespace,
		Cache: cache.Options{
			SyncPeriod:        &resyncPeriod,
			DefaultNamespaces: map[string]cache.Config{namespace: {}},
			ByObject: map[client.Object]cache.ByObject{
				&tinkv1.Hardware{}: {Label: selector},
			},
		},
	})
	if err != nil {
		log.Error("unable to create manager", "err", err)
		os.Exit(1)
	}

	reconciler := &janitor.Reconciler{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorder(janitor.Name),
		Opts: janitor.Options{
			ClearOSMetadata:  clearOSMetadata,
			BaselineAllowPXE: baselineAllow,
		},
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		log.Error("unable to set up controller", "err", err)
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error("unable to set up health check", "err", err)
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error("unable to set up ready check", "err", err)
		os.Exit(1)
	}

	log.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error("manager exited with error", "err", err)
		os.Exit(1)
	}
}
