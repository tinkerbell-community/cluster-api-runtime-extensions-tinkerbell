// The talos-image-resolver manager resolves a per-machine Talos Image Factory schematic and
// version and writes the image identity onto claimed Tinkerbell Hardware
// (spec.metadata.instance.operating_system + a talos.tinkerbell.org/installer-image annotation),
// coexisting with CAPT's own status resolution. See docs/runtime-extensions-migration.md §3.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/go-logr/logr"
	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/tinkerbell-community/tinkerbell-bmc-discovery-controller/internal/logging"
	"github.com/tinkerbell-community/tinkerbell-bmc-discovery-controller/internal/resolve"
)

// Build metadata injected by goreleaser via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	builtBy = "unknown"
)

// options carries the parsed flags.
type options struct {
	watchNamespace string
	factoryURL     string
	tootlesURL     string
	tinkerbellIP   string
	concurrency    int
	leaderElect    bool
	metricsAddr    string
	probeAddr      string
	logLevel       string
	logFormat      string
	workflowGate   bool
	webhookPort    int
	webhookCertDir string
}

func registerFlags(o *options) {
	flag.StringVar(&o.watchNamespace, "watch-namespace", "", "Namespace to watch; empty watches all namespaces.")
	flag.StringVar(&o.factoryURL, "factory-url", "", "Image Factory base URL; empty selects the public factory.")
	flag.StringVar(&o.tootlesURL, "tootles-url", "", "Tootles base URL (e.g. http://10.0.0.1:7080) for the talos.config kernel arg.")
	flag.StringVar(&o.tinkerbellIP, "tinkerbell-ip", "", "Tinkerbell IP; used to build the tootles URL when --tootles-url is unset.")
	flag.IntVar(&o.concurrency, "concurrency", 4, "Maximum concurrent reconciles.")
	flag.BoolVar(&o.leaderElect, "leader-elect", true, "Enable leader election.")
	flag.StringVar(&o.metricsAddr, "metrics-bind-address", ":8080", "Metrics endpoint bind address.")
	flag.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "Health probe bind address.")
	flag.StringVar(&o.logLevel, "log-level", "info", "Log level: debug, info, warn, or error.")
	flag.StringVar(&o.logFormat, "log-format", "json", "Log format: json or text.")
	flag.BoolVar(&o.workflowGate, "enable-workflow-gate", true,
		"Serve the mandatory Workflow-CREATE admission gate that holds renders until operating_system is written.")
	flag.IntVar(&o.webhookPort, "webhook-port", 9443, "Webhook server port (workflow gate).")
	flag.StringVar(&o.webhookCertDir, "webhook-cert-dir", "",
		"Directory holding tls.crt/tls.key for the webhook server; empty uses the controller-runtime default.")
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
	log.Info(resolve.Name, "version", version, "commit", commit, "date", date, "builtBy", builtBy)

	if err := run(opts, log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run(opts options, log *slog.Logger) error {
	// FAIL FAST: TootlesUserDataURL is REQUIRED, before any manager/client setup. A zero-value
	// CustomizationConfig would make SignalsFromHardware silently omit the talos.config kernel
	// arg, booting every raw-disk node into maintenance mode instead of provisioning it. Never
	// construct CustomizationConfig without validating this first.
	tootlesUserData, err := resolve.TootlesUserDataURL(opts.tootlesURL, opts.tinkerbellIP)
	if err != nil {
		return fmt.Errorf("invalid tootles configuration: %w", err)
	}

	factory := opts.factoryURL
	if factory == "" {
		factory = resolve.DefaultFactoryURL
	}

	mgr, err := buildManager(opts)
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	reconciler := &resolve.Reconciler{
		Client:        mgr.GetClient(),
		Recorder:      mgr.GetEventRecorder(resolve.Name),
		Registrar:     resolve.NewRegistrar(opts.factoryURL),
		Policy:        resolve.VersionPolicy{Versions: resolve.NewVersionResolver(opts.factoryURL)},
		Customization: resolve.CustomizationConfig{TootlesUserDataURL: tootlesUserData},
		FactoryURL:    factory,
	}
	if err := reconciler.SetupWithManager(mgr, opts.concurrency); err != nil {
		return fmt.Errorf("setting up controller: %w", err)
	}
	if opts.workflowGate {
		// The gate is mandatory in production (spec §3.7): without it a Workflow that
		// renders before the resolver writes operating_system bricks the machine. The
		// flag exists for bring-up ordering only, never as a steady-state posture.
		if err := (&resolve.WorkflowGate{}).SetupWithManager(mgr); err != nil {
			return fmt.Errorf("setting up workflow gate: %w", err)
		}
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("setting up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("setting up ready check: %w", err)
	}

	log.Info("starting manager")
	return mgr.Start(ctrl.SetupSignalHandler())
}

func buildManager(opts options) (ctrl.Manager, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, clusterv1.AddToScheme, tinkv1.AddToScheme} {
		if err := add(scheme); err != nil {
			return nil, fmt.Errorf("building scheme: %w", err)
		}
	}

	cacheOptions := cache.Options{}
	if opts.watchNamespace != "" {
		cacheOptions.DefaultNamespaces = map[string]cache.Config{opts.watchNamespace: {}}
	}

	managerOptions := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: opts.metricsAddr},
		HealthProbeBindAddress: opts.probeAddr,
		LeaderElection:         opts.leaderElect,
		LeaderElectionID:       "talos-image-resolver.resolve.tinkerbell.org",
		Cache:                  cacheOptions,
	}
	if opts.workflowGate {
		// The webhook server is a non-leader-election runnable: every replica answers
		// admission, so the gate stays available under failurePolicy: Fail (spec §2.4).
		managerOptions.WebhookServer = webhook.NewServer(webhook.Options{
			Port:    opts.webhookPort,
			CertDir: opts.webhookCertDir,
		})
	}

	return ctrl.NewManager(ctrl.GetConfigOrDie(), managerOptions)
}
