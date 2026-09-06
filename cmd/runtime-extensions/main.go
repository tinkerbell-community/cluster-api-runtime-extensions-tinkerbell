// The runtime-extensions manager is the consolidated CAREN-analog binary of
// cluster-api-runtime-extensions-tinkerbell (spec §2): one ctrl.Manager whose
// webhook server hosts the admission webhooks today and the CAPI runtime
// (topology/lifecycle) hooks when ClusterClass lands — it MUST register zero
// in-place hooks (CABPT owns CanUpdateMachine/UpdateMachine; a second
// registration breaks in-place updates cluster-wide) — plus the plain
// leader-elected controllers: talos-image-resolver, talos-machine-teardown,
// tinkerbell-hardware-janitor, talos-upgrade-coordinator. Every component keeps
// its own SSA field-manager identity; consolidation is a process boundary, not
// an ownership change (spec §2.6). BMC discovery stays a separate deployable
// (cmd/bmc-discovery) — its hostNetwork/mDNS posture cannot share a
// webhook-serving pod (spec §2.1).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/component-base/featuregate"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/internal/janitor"
	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/internal/logging"
	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/internal/resolve"
	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/internal/teardown"
	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/internal/upgrade"
)

// componentName is the provider identity: leader-election ID, handler-name
// prefix, and (Wave 2) the ExtensionConfig name derive from it.
const componentName = "cluster-api-runtime-extensions-tinkerbell"

// credentialGCInterval paces the cached-talosconfig garbage collection.
const credentialGCInterval = 10 * time.Minute

// Feature gates, one per hosted component family. All on by default; gates
// exist for staged bring-up and incident isolation, not as a config surface.
const (
	FeatureImageResolver      featuregate.Feature = "TalosImageResolver"
	FeatureWorkflowGate       featuregate.Feature = "WorkflowCreateGate"
	FeatureMachineTeardown    featuregate.Feature = "MachineTeardown"
	FeatureHardwareJanitor    featuregate.Feature = "HardwareJanitor"
	FeatureUpgradeCoordinator featuregate.Feature = "UpgradeCoordinator"
)

func newFeatureGates() featuregate.MutableFeatureGate {
	gates := featuregate.NewFeatureGate()
	if err := gates.Add(map[featuregate.Feature]featuregate.FeatureSpec{
		FeatureImageResolver:      {Default: true, PreRelease: featuregate.Beta},
		FeatureWorkflowGate:       {Default: true, PreRelease: featuregate.Beta},
		FeatureMachineTeardown:    {Default: true, PreRelease: featuregate.Beta},
		FeatureHardwareJanitor:    {Default: true, PreRelease: featuregate.Beta},
		FeatureUpgradeCoordinator: {Default: true, PreRelease: featuregate.Beta},
	}); err != nil {
		panic(err)
	}
	return gates
}

// Build metadata injected by goreleaser via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	builtBy = "unknown"
)

// options carries the parsed flags. Shared flags keep their historical names;
// component-specific flags are prefixed where two components would collide.
type options struct {
	watchNamespace    string
	cacheResyncPeriod time.Duration
	leaderElect       bool
	metricsAddr       string
	probeAddr         string
	logLevel          string
	logFormat         string

	webhookPort    int
	webhookCertDir string

	// resolver
	factoryURL          string
	tootlesURL          string
	tinkerbellIP        string
	resolverConcurrency int

	// teardown
	cacheNamespace      string
	etcdTimeout         time.Duration
	resetTimeout        time.Duration
	etcdCallTimeout     time.Duration
	resetCallTimeout    time.Duration
	teardownConcurrency int

	// janitor
	hardwareNamespace string
	clearOSMetadata   bool
	baselineAllowPXE  bool

	// upgrade coordinator
	gateRequeueInterval time.Duration
	gateIgnoreNodes     string
	reconcileTimeout    time.Duration
	upgradeResyncPeriod time.Duration
	upgradeDryRun       bool
	manifestsForce      bool
	manifestsNoPrune    bool
}

func registerFlags(o *options, gates featuregate.MutableFeatureGate) {
	flag.StringVar(&o.watchNamespace, "watch-namespace", "", "Namespace to watch for CAPI/Tinkerbell objects; empty watches all namespaces.")
	flag.DurationVar(&o.cacheResyncPeriod, "cache-resync-period", time.Hour, "Informer resync; bounds the janitor's catch-up window for missed events.")
	flag.BoolVar(&o.leaderElect, "leader-elect", true, "Enable leader election (controllers only; webhooks serve on every replica).")
	flag.StringVar(&o.metricsAddr, "metrics-bind-address", ":8080", "Metrics endpoint bind address.")
	flag.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "Health probe bind address.")
	flag.StringVar(&o.logLevel, "log-level", "info", "Log level: debug, info, warn, or error.")
	flag.StringVar(&o.logFormat, "log-format", "json", "Log format: json or text.")
	flag.Func("feature-gates", "Comma-separated key=value pairs toggling component families, e.g. UpgradeCoordinator=false.", gates.Set)

	flag.IntVar(&o.webhookPort, "webhook-port", 9443, "Webhook server port.")
	flag.StringVar(&o.webhookCertDir, "webhook-cert-dir", "", "Directory holding tls.crt/tls.key; empty uses the controller-runtime default.")

	flag.StringVar(&o.factoryURL, "factory-url", "", "Image Factory base URL; empty selects the public factory.")
	flag.StringVar(&o.tootlesURL, "tootles-url", "", "Tootles base URL (e.g. http://10.0.0.1:7080) for the talos.config kernel arg.")
	flag.StringVar(&o.tinkerbellIP, "tinkerbell-ip", "", "Tinkerbell IP; used to build the tootles URL when --tootles-url is unset.")
	flag.IntVar(&o.resolverConcurrency, "resolver-concurrency", 4, "Maximum concurrent resolver reconciles.")

	flag.StringVar(&o.cacheNamespace, "cache-namespace", "", "Namespace for cached talosconfig secrets (the manager's own namespace); defaults to POD_NAMESPACE.")
	flag.DurationVar(&o.etcdTimeout, "etcd-timeout", 2*time.Minute, "Teardown etcd phase deadline, measured from the pre-terminate-observed-at annotation.")
	flag.DurationVar(&o.resetTimeout, "reset-timeout", 5*time.Minute, "Teardown reset phase deadline (includes the etcd phase).")
	flag.DurationVar(&o.etcdCallTimeout, "etcd-call-timeout", 10*time.Second, "Per-RPC timeout for Talos etcd operations.")
	flag.DurationVar(&o.resetCallTimeout, "reset-call-timeout", 30*time.Second, "Per-RPC timeout for the Talos reset.")
	flag.IntVar(&o.teardownConcurrency, "teardown-concurrency", 4, "Maximum concurrent teardown reconciles (etcd work is serialized per cluster regardless).")

	flag.StringVar(&o.hardwareNamespace, "hardware-namespace", "tinkerbell", "Namespace the janitor scrubs released Hardware in.")
	flag.BoolVar(&o.clearOSMetadata, "clear-os-metadata", true, "Clear metadata.instance.operating_system and .state on scrub; disable in environments still on terraform-written OS metadata.")
	flag.BoolVar(&o.baselineAllowPXE, "baseline-allow-pxe", false, "Parked allowPXE value asserted on released hardware's netboot-bearing interfaces.")

	flag.DurationVar(&o.gateRequeueInterval, "gate-requeue-interval", time.Minute, "Requeue interval while the upgrade convergence gate is blocked.")
	flag.StringVar(&o.gateIgnoreNodes, "gate-ignore-nodes", "", "Comma-separated workload Node names skipped by the kubelet-version gate.")
	flag.DurationVar(&o.reconcileTimeout, "reconcile-timeout", 5*time.Minute, "Bound on the post-apply wait for synced manifests.")
	flag.DurationVar(&o.upgradeResyncPeriod, "upgrade-resync-period", 0, "Re-run the manifest sync at the current pair to repair drift; 0 disables.")
	flag.BoolVar(&o.upgradeDryRun, "upgrade-dry-run", false, "Diff and report bootstrap manifests without writing to the workload cluster.")
	flag.BoolVar(&o.manifestsForce, "manifests-force", false, "Recreate manifest objects with immutable field changes.")
	flag.BoolVar(&o.manifestsNoPrune, "manifests-no-prune", false, "Disable pruning of objects Talos no longer renders.")
}

func main() {
	var opts options
	gates := newFeatureGates()
	registerFlags(&opts, gates)
	flag.Parse()
	if opts.cacheNamespace == "" {
		opts.cacheNamespace = os.Getenv("POD_NAMESPACE")
	}

	root, err := logging.New(opts.logLevel, opts.logFormat, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctrl.SetLogger(logr.FromSlogHandler(root.Handler()))
	log := logging.Component(root, "setup")
	log.Info(componentName, "version", version, "commit", commit, "date", date, "builtBy", builtBy)

	if err := run(opts, gates, root, log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run(opts options, gates featuregate.FeatureGate, root, log *slog.Logger) error {
	mgr, err := buildManager(opts, gates)
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	setups := []struct {
		gate  featuregate.Feature
		wire  func(ctrl.Manager) error
		title string
	}{
		{FeatureImageResolver, func(m ctrl.Manager) error { return setupResolver(m, opts) }, "talos-image-resolver"},
		{FeatureWorkflowGate, setupWorkflowGate, "workflow-create gate"},
		{FeatureMachineTeardown, func(m ctrl.Manager) error { return setupTeardown(m, opts, root) }, "talos-machine-teardown"},
		{FeatureHardwareJanitor, func(m ctrl.Manager) error { return setupJanitor(m, opts) }, "tinkerbell-hardware-janitor"},
		{FeatureUpgradeCoordinator, func(m ctrl.Manager) error { return setupUpgrade(m, opts) }, "talos-upgrade-coordinator"},
	}
	for _, s := range setups {
		if !gates.Enabled(s.gate) {
			log.Warn("component disabled by feature gate", "component", s.title, "gate", string(s.gate))
			continue
		}
		if err := s.wire(mgr); err != nil {
			return fmt.Errorf("setting up %s: %w", s.title, err)
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

func setupResolver(mgr ctrl.Manager, opts options) error {
	// FAIL FAST: a zero-value CustomizationConfig would silently omit the
	// talos.config kernel arg and boot every raw-disk node into maintenance mode.
	tootlesUserData, err := resolve.TootlesUserDataURL(opts.tootlesURL, opts.tinkerbellIP)
	if err != nil {
		return fmt.Errorf("invalid tootles configuration: %w", err)
	}
	factory := opts.factoryURL
	if factory == "" {
		factory = resolve.DefaultFactoryURL
	}
	reconciler := &resolve.Reconciler{
		Client:        mgr.GetClient(),
		Recorder:      mgr.GetEventRecorder(resolve.Name),
		Registrar:     resolve.NewRegistrar(opts.factoryURL),
		Policy:        resolve.VersionPolicy{Versions: resolve.NewVersionResolver(opts.factoryURL)},
		Customization: resolve.CustomizationConfig{TootlesUserDataURL: tootlesUserData},
		FactoryURL:    factory,
	}
	return reconciler.SetupWithManager(mgr, opts.resolverConcurrency)
}

func setupWorkflowGate(mgr ctrl.Manager) error {
	return (&resolve.WorkflowGate{}).SetupWithManager(mgr)
}

func setupTeardown(mgr ctrl.Manager, opts options, root *slog.Logger) error {
	if opts.cacheNamespace == "" {
		return fmt.Errorf("--cache-namespace is required when POD_NAMESPACE is unset")
	}
	credentials := &teardown.CredentialCache{Client: mgr.GetClient(), Namespace: opts.cacheNamespace}
	reconciler := &teardown.Reconciler{
		Client:           mgr.GetClient(),
		Recorder:         mgr.GetEventRecorder(teardown.Name),
		TalosFactory:     teardown.NewTalosClient,
		Credentials:      credentials,
		EtcdTimeout:      opts.etcdTimeout,
		ResetTimeout:     opts.resetTimeout,
		EtcdCallTimeout:  opts.etcdCallTimeout,
		ResetCallTimeout: opts.resetCallTimeout,
	}
	if err := reconciler.SetupWithManager(mgr, opts.teardownConcurrency); err != nil {
		return err
	}
	return mgr.Add(credentialGC(credentials, root))
}

func setupJanitor(mgr ctrl.Manager, opts options) error {
	reconciler := &janitor.Reconciler{
		Client:    mgr.GetClient(),
		Recorder:  mgr.GetEventRecorder(janitor.Name),
		Namespace: opts.hardwareNamespace,
		Opts: janitor.Options{
			ClearOSMetadata:  opts.clearOSMetadata,
			BaselineAllowPXE: opts.baselineAllowPXE,
		},
	}
	return reconciler.SetupWithManager(mgr)
}

func setupUpgrade(mgr ctrl.Manager, opts options) error {
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
			ResyncPeriod:        opts.upgradeResyncPeriod,
			IgnoreNodes:         ignore,
			DryRun:              opts.upgradeDryRun,
			ManifestsForce:      opts.manifestsForce,
			ManifestsNoPrune:    opts.manifestsNoPrune,
		},
	}
	return reconciler.SetupWithManager(mgr, 1)
}

// credentialGC returns the periodic (and startup) garbage collector for cached
// talosconfig secrets whose cluster and machines are gone.
func credentialGC(credentials *teardown.CredentialCache, root *slog.Logger) manager.Runnable {
	return manager.RunnableFunc(func(ctx context.Context) error {
		log := logging.Component(root, "credential-gc")
		ticker := time.NewTicker(credentialGCInterval)
		defer ticker.Stop()
		for {
			if err := credentials.GC(ctx); err != nil {
				log.Warn("credential cache garbage collection failed", "err", err)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
	})
}

func buildManager(opts options, gates featuregate.FeatureGate) (ctrl.Manager, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, clusterv1.AddToScheme, tinkv1.AddToScheme} {
		if err := add(scheme); err != nil {
			return nil, fmt.Errorf("building scheme: %w", err)
		}
	}

	cacheOptions := cache.Options{SyncPeriod: &opts.cacheResyncPeriod}
	if opts.watchNamespace != "" {
		cacheOptions.DefaultNamespaces = map[string]cache.Config{opts.watchNamespace: {}}
	}

	managerOptions := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: opts.metricsAddr},
		HealthProbeBindAddress: opts.probeAddr,
		LeaderElection:         opts.leaderElect,
		LeaderElectionID:       componentName + ".tinkerbell.org",
		Cache:                  cacheOptions,
		Client: client.Options{
			// Secrets and Hardware are read directly, never through the cache:
			// teardown's RBAC grants get/list only, a Secret informer would hold
			// every cluster secret in memory, the janitor's conflict-guarded
			// Update wants fresh reads, and the resolver's optimistic
			// resourceVersion precondition is strictly safer uncached.
			Cache: &client.CacheOptions{
				DisableFor: []client.Object{&corev1.Secret{}, &tinkv1.Hardware{}},
			},
		},
	}
	if gates.Enabled(FeatureWorkflowGate) {
		// The webhook server is a non-leader-election runnable: every replica
		// answers admission, so failurePolicy Fail stays available (spec §2.4).
		managerOptions.WebhookServer = webhook.NewServer(webhook.Options{
			Port:    opts.webhookPort,
			CertDir: opts.webhookCertDir,
		})
	}

	return ctrl.NewManager(ctrl.GetConfigOrDie(), managerOptions)
}
