// The bmc-manager manages Intel AMT devices as first-class BMCs: it verifies
// their credentials, collects inventory, registers them as Tinkerbell Machine
// and Hardware resources, and serves the fleet through a Redfish aggregator.
//
// See docs/bmc-manager.md for the design and the decisions behind it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-logr/logr"
	bmcv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/bmc"
	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	amtv1 "github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/api/amt/v1alpha1"
	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/internal/amtenroll"
	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/internal/logging"
	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/internal/redfish"
)

// Build metadata injected by goreleaser via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	builtBy = "unknown"
)

func main() {
	cfg := &config{}
	cfg.bindFlags(flag.CommandLine)
	flag.Parse()

	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// config holds every command-line option.
type config struct {
	namespace         string
	defaultProfile    string
	deviceTimeout     time.Duration
	resyncInterval    time.Duration
	retryInterval     time.Duration
	facilityCode      string
	autoEnrollment    bool
	redfishAddr       string
	redfishEnabled    bool
	serviceUUID       string
	enforceSecureBoot bool
	imageRoot         string
	imageBaseURL      string
	tlsCertFile       string
	tlsKeyFile        string
	leaderElect       bool
	metricsAddr       string
	probeAddr         string
	logLevel          string
	logFormat         string
}

// bindFlags registers every option on a flag set.
func (c *config) bindFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.namespace, "namespace", "tink", "Namespace for AMTDevice resources and everything created from them.")
	fs.StringVar(&c.defaultProfile, "default-profile", "default", "AMTProfile used by devices that do not name one.")
	fs.DurationVar(&c.deviceTimeout, "device-timeout", 30*time.Second, "Timeout for one AMT operation.")
	fs.DurationVar(&c.resyncInterval, "resync-interval", time.Hour, "How often a healthy device is re-read.")
	fs.DurationVar(&c.retryInterval, "retry-interval", 5*time.Minute, "How soon an unreachable device is retried.")
	fs.StringVar(&c.facilityCode, "facility-code", "onprem", "metadata.facility.facility_code set on created Hardware; empty omits it.")
	fs.BoolVar(&c.autoEnrollment, "auto-enrollment", false, "Enable Tinkerbell auto enrollment on created Hardware.")
	fs.BoolVar(&c.redfishEnabled, "redfish", true, "Serve the Redfish aggregator.")
	fs.StringVar(&c.redfishAddr, "redfish-bind-address", ":8443", "Redfish aggregator bind address.")
	fs.StringVar(&c.serviceUUID, "redfish-service-uuid", "", "Stable UUID advertised in the Redfish service root. Generated per start when empty, which makes the service appear to change identity across restarts.")
	fs.BoolVar(&c.enforceSecureBoot, "enforce-secure-boot", false, "Require virtual-media boot images to be signed.")
	fs.StringVar(&c.imageRoot, "image-root", "", "Directory of boot images served for UEFI HTTPS boot. Empty disables the image server.")
	fs.StringVar(&c.imageBaseURL, "image-base-url", "", "External https:// base URL of the image server, as reachable from devices.")
	fs.StringVar(&c.tlsCertFile, "tls-cert-file", "", "TLS certificate for the Redfish and image endpoints. Empty serves plaintext, which AMT will not accept for HTTPS boot.")
	fs.StringVar(&c.tlsKeyFile, "tls-key-file", "", "TLS private key.")
	fs.BoolVar(&c.leaderElect, "leader-elect", false, "Enable leader election.")
	fs.StringVar(&c.metricsAddr, "metrics-bind-address", ":8080", "Metrics endpoint bind address.")
	fs.StringVar(&c.probeAddr, "health-probe-bind-address", ":8081", "Health probe bind address.")
	fs.StringVar(&c.logLevel, "log-level", "info", "Log level: debug, info, warn, or error.")
	fs.StringVar(&c.logFormat, "log-format", "json", "Log format: json or text.")
}

func run(c *config) error {
	root, err := logging.New(c.logLevel, c.logFormat, os.Stderr)
	if err != nil {
		return err
	}
	ctrl.SetLogger(logr.FromSlogHandler(root.Handler()))
	log := logging.Component(root, "setup")
	log.Info("bmc-manager", "version", version, "commit", commit, "date", date, "builtBy", builtBy)

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		bmcv1.AddToScheme,
		tinkv1.AddToScheme,
		amtv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			return fmt.Errorf("building scheme: %w", err)
		}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: c.metricsAddr},
		HealthProbeBindAddress:  c.probeAddr,
		LeaderElection:          c.leaderElect,
		LeaderElectionID:        "bmc-manager.amt.tinkerbell.org",
		LeaderElectionNamespace: c.namespace,
		Cache: cache.Options{
			// AMTProfile is cluster-scoped, so it cannot be restricted to a
			// c.namespace; everything else is namespaced.
			DefaultNamespaces: map[string]cache.Config{c.namespace: {}},
			ByObject: map[client.Object]cache.ByObject{
				&amtv1.AMTProfile{}: {Namespaces: nil},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	reconciler := &amtenroll.Reconciler{
		Client:         mgr.GetClient(),
		DefaultProfile: c.defaultProfile,
		Timeout:        c.deviceTimeout,
		ResyncInterval: c.resyncInterval,
		RetryInterval:  c.retryInterval,
		FacilityCode:   c.facilityCode,
		AutoEnrollment: c.autoEnrollment,
		Now:            time.Now,
		Log:            logging.Component(root, "amtenroll"),
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setting up the enrollment reconciler: %w", err)
	}

	if c.redfishEnabled {
		if err := addRedfish(mgr, redfishOptions{
			namespace:         c.namespace,
			addr:              c.redfishAddr,
			serviceUUID:       c.serviceUUID,
			enforceSecureBoot: c.enforceSecureBoot,
			deviceTimeout:     c.deviceTimeout,
			imageRoot:         c.imageRoot,
			imageBaseURL:      c.imageBaseURL,
			certFile:          c.tlsCertFile,
			keyFile:           c.tlsKeyFile,
			log:               root,
		}); err != nil {
			return err
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("setting up the health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("setting up the ready check: %w", err)
	}

	log.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("manager exited with error: %w", err)
	}
	return nil
}

type redfishOptions struct {
	namespace         string
	addr              string
	serviceUUID       string
	enforceSecureBoot bool
	deviceTimeout     time.Duration
	imageRoot         string
	imageBaseURL      string
	certFile          string
	keyFile           string
	log               *slog.Logger
}

// addRedfish wires the aggregator and, when configured, the boot image server
// onto one listener managed by the controller manager.
func addRedfish(mgr manager.Manager, opts redfishOptions) error {
	log := logging.Component(opts.log, "redfish")

	aggregator := &redfish.Server{
		Store: &redfish.KubeStore{
			Client:    mgr.GetClient(),
			Namespace: opts.namespace,
			Timeout:   opts.deviceTimeout,
		},
		ServiceUUID:       opts.serviceUUID,
		EnforceSecureBoot: opts.enforceSecureBoot,
		Log:               log,
	}

	mux := http.NewServeMux()
	mux.Handle("/", aggregator.Handler())

	if opts.imageRoot != "" {
		images := &redfish.ImageServer{
			Root:    opts.imageRoot,
			BaseURL: opts.imageBaseURL,
			Log:     logging.Component(opts.log, "images"),
		}
		// Validated at startup rather than on first use: a misconfigured
		// image server produces a boot that succeeds at every step and then
		// silently does not happen, which is the hardest failure here to
		// diagnose.
		if err := images.Validate(); err != nil {
			return err
		}
		mux.Handle(redfish.ImagePrefix, images.Handler())
		log.Info("serving boot images", "root", opts.imageRoot, "baseURL", opts.imageBaseURL)
	}

	if opts.certFile == "" {
		// AMT will not fetch a boot image over plaintext, and the aggregator
		// carries credentials-equivalent authority over the fleet. Running
		// without TLS is supported for local development but must be loud.
		log.Warn("serving Redfish without TLS; AMT will not perform HTTPS boot against this endpoint")
	}

	return mgr.Add(&httpRunnable{
		addr:     opts.addr,
		handler:  mux,
		certFile: opts.certFile,
		keyFile:  opts.keyFile,
		log:      log,
	})
}

// httpRunnable adapts an HTTP server to the manager's Runnable interface so it
// shares the manager's lifecycle and shuts down with it.
type httpRunnable struct {
	addr     string
	handler  http.Handler
	certFile string
	keyFile  string
	log      *slog.Logger
}

func (h *httpRunnable) Start(ctx context.Context) error {
	server := &http.Server{
		Addr:              h.addr,
		Handler:           h.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		h.log.Info("listening", "address", h.addr, "tls", h.certFile != "")
		var err error
		if h.certFile != "" {
			err = server.ListenAndServeTLS(h.certFile, h.keyFile)
		} else {
			err = server.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

// NeedLeaderElection reports false: the aggregator is a read-mostly API
// surface, and every replica should serve it rather than only the leader.
func (h *httpRunnable) NeedLeaderElection() bool { return false }
