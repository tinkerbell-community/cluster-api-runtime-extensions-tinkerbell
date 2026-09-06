package upgrade

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	"github.com/cosi-project/runtime/pkg/state"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// secretRequeue paces retries while a cluster secret is missing or malformed.
const secretRequeue = 5 * time.Minute

// Options carry the coordinator's flag-derived configuration.
type Options struct {
	GateRequeueInterval time.Duration
	ReconcileTimeout    time.Duration
	ResyncPeriod        time.Duration
	IgnoreNodes         sets.Set[string]
	DryRun              bool
	ManifestsForce      bool
	ManifestsNoPrune    bool
}

// Reconciler implements the level-triggered sync loop: compute the desired
// (k8s, talos) pair, compare with the checkpoint annotation, evaluate the
// convergence gate, and only then sync bootstrap manifests into the workload
// cluster. The externals — workload clientset, Talos API, the sync itself — are
// injected so the loop is testable without any cluster.
type Reconciler struct {
	client.Client
	Recorder events.EventRecorder

	NewWorkload func(kubeconfig []byte) (kubernetes.Interface, *rest.Config, error)
	NewTalos    func(ctx context.Context, talosconfig []byte, endpoints []string) (TalosConn, error)
	Sync        func(ctx context.Context, cosi state.State, restCfg *rest.Config, talosVersion semver.Version, o SyncOptions) (SyncReport, error)

	Opts Options

	// Nudges, when set, feeds lifecycle-hook reconcile requests (SP-10).
	Nudges chan event.GenericEvent
}

// Reconcile implements the flow of docs/talos-upgrade-coordinator.md §Reconcile flow.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	tcp := &unstructured.Unstructured{}
	tcp.SetGroupVersionKind(TalosControlPlaneGVK)
	if err := r.Get(ctx, req.NamespacedName, tcp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// No finalizer by design: the coordinator keeps no state needing cleanup —
	// a torn-down cluster simply stops being synced.
	if tcp.GetDeletionTimestamp() != nil {
		return ctrl.Result{}, nil
	}

	cluster, err := ClusterNameFor(tcp)
	if err != nil {
		return ctrl.Result{}, err
	}
	target, err := TCPVersion(tcp)
	if err != nil {
		return ctrl.Result{}, err
	}
	targetVersion, err := semver.ParseTolerant(target)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("parsing target version %q: %w", target, err)
	}

	kubeconfig, talosconfig, ok := r.clusterSecrets(ctx, tcp, cluster)
	if !ok {
		return ctrl.Result{RequeueAfter: secretRequeue}, nil
	}

	endpoints, blocked := r.upToDateCPEndpoints(ctx, tcp, cluster)
	if blocked != "" {
		r.gateBlocked(tcp, blocked)
		return ctrl.Result{RequeueAfter: r.Opts.GateRequeueInterval}, nil
	}

	conn, err := r.NewTalos(ctx, talosconfig, endpoints)
	if err != nil {
		r.Recorder.Eventf(tcp, nil, "Warning", "TalosUnreachable", "TalosUnreachable", "building talos client: %v", err)
		return ctrl.Result{}, err
	}
	defer func() { _ = conn.Close() }()
	observed, err := conn.ObservedVersion(ctx, endpoints[0])
	if err != nil {
		r.Recorder.Eventf(tcp, nil, "Warning", "TalosUnreachable", "TalosUnreachable", "reading talos version from %s: %v", endpoints[0], err)
		return ctrl.Result{}, err
	}

	pair := Pair{K8s: target, Talos: "v" + observed.String()}
	force := ForceSyncRequested(tcp)
	if LastSynced(tcp) == pair.String() && !force {
		if r.Opts.ResyncPeriod > 0 {
			return r.sync(ctx, tcp, kubeconfig, conn, endpoints, observed, targetVersion, pair, false)
		}
		return ctrl.Result{}, nil
	}

	return r.sync(ctx, tcp, kubeconfig, conn, endpoints, observed, targetVersion, pair, force)
}

// sync evaluates the gate and, if it passes (or is force-bypassed), fetches and
// applies the bootstrap manifests, then checkpoints.
func (r *Reconciler) sync(ctx context.Context, tcp *unstructured.Unstructured, kubeconfig []byte, conn TalosConn,
	endpoints []string, observed semver.Version, targetVersion semver.Version, pair Pair, force bool,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	workload, restCfg, err := r.NewWorkload(kubeconfig)
	if err != nil {
		r.Recorder.Eventf(tcp, nil, "Warning", "WorkloadUnreachable", "WorkloadUnreachable", "building workload client: %v", err)
		return ctrl.Result{}, err
	}

	gate, err := EvaluateGate(ctx, r.Client, workload, mustClusterName(tcp), tcp.GetNamespace(), targetVersion, r.Opts.IgnoreNodes)
	if err != nil {
		r.Recorder.Eventf(tcp, nil, "Warning", "WorkloadUnreachable", "WorkloadUnreachable", "evaluating convergence gate: %v", err)
		return ctrl.Result{}, err
	}
	for _, name := range gate.FallbackMachines {
		r.Recorder.Eventf(tcp, nil, "Normal", "UpToDateFallback", "UpToDateFallback",
			"machine %s has no UpToDate condition; judged by spec.version minor equality", name)
	}
	if !gate.Converged {
		if !force {
			r.gateBlocked(tcp, strings.Join(gate.Blockers, "; "))
			return ctrl.Result{RequeueAfter: r.Opts.GateRequeueInterval}, nil
		}
		r.Recorder.Eventf(tcp, nil, "Normal", "ForceSyncHonored", "ForceSyncHonored",
			"gate bypassed by %s despite: %s", ForceSyncAnnotation, strings.Join(gate.Blockers, "; "))
	}

	report, err := r.Sync(WithTalosNode(ctx, endpoints[0]), conn.State(), restCfg, observed, SyncOptions{
		DryRun:           r.Opts.DryRun,
		Force:            r.Opts.ManifestsForce,
		NoPrune:          r.Opts.ManifestsNoPrune,
		ReconcileTimeout: r.Opts.ReconcileTimeout,
		Log: func(msg string, args ...any) {
			log.V(1).Info(msg, args...)
		},
	})
	if err != nil {
		syncTotal.WithLabelValues("error").Inc()
		r.Recorder.Eventf(tcp, nil, "Warning", "ManifestsSyncFailed", "ManifestsSyncFailed", "syncing bootstrap manifests for %s: %v", pair, err)
		return ctrl.Result{}, err
	}

	if r.Opts.DryRun {
		r.Recorder.Eventf(tcp, nil, "Normal", "ManifestsDiffed", "ManifestsDiffed",
			"dry-run: %d manifest changes pending for %s (no writes, no checkpoint)", len(report.Diffs), pair)
		return ctrl.Result{}, nil
	}

	if err := WriteCheckpoint(ctx, r.Client, tcp, pair.String()); err != nil {
		return ctrl.Result{}, err
	}
	if force {
		if err := ClearForceSync(ctx, r.Client, tcp); err != nil {
			return ctrl.Result{}, err
		}
	}
	syncTotal.WithLabelValues("success").Inc()
	lastSyncTimestamp.SetToCurrentTime()
	manifestsPruned.Add(float64(report.Pruned))
	r.Recorder.Eventf(tcp, nil, "Normal", "ManifestsSynced", "ManifestsSynced",
		"%s: %d created, %d configured, %d unchanged, %d pruned",
		pair, report.Created, report.Configured, report.Unchanged, report.Pruned)

	if r.Opts.ResyncPeriod > 0 {
		return ctrl.Result{RequeueAfter: r.Opts.ResyncPeriod}, nil
	}
	return ctrl.Result{}, nil
}

// clusterSecrets loads the kubeconfig and talosconfig; a missing secret events
// and paces a quiet requeue rather than erroring (the secret may simply not be
// seeded yet).
func (r *Reconciler) clusterSecrets(ctx context.Context, tcp *unstructured.Unstructured, cluster string) (kubeconfig, talosconfig []byte, ok bool) {
	kubeconfig, err := WorkloadKubeconfig(ctx, r.Client, tcp.GetNamespace(), cluster)
	if err != nil {
		r.Recorder.Eventf(tcp, nil, "Warning", "SecretMissing", "SecretMissing", "workload kubeconfig: %v", err)
		gateBlockedMetric.WithLabelValues("kubeconfig").Inc()
		return nil, nil, false
	}
	talosconfig, err = TalosClientConfig(ctx, r.Client, tcp.GetNamespace(), cluster)
	if err != nil {
		r.Recorder.Eventf(tcp, nil, "Warning", "SecretMissing", "SecretMissing", "talosconfig: %v", err)
		gateBlockedMetric.WithLabelValues("talosconfig").Inc()
		return nil, nil, false
	}
	return kubeconfig, talosconfig, true
}

// upToDateCPEndpoints returns internal addresses of UpToDate control-plane
// Machines, or a blocker description when none qualify.
func (r *Reconciler) upToDateCPEndpoints(ctx context.Context, tcp *unstructured.Unstructured, cluster string) ([]string, string) {
	machines := &clusterv1.MachineList{}
	if err := r.List(ctx, machines, client.InNamespace(tcp.GetNamespace()),
		client.MatchingLabels{clusterv1.ClusterNameLabel: cluster},
		client.HasLabels{clusterv1.MachineControlPlaneLabel}); err != nil {
		return nil, fmt.Sprintf("listing control-plane machines: %v", err)
	}

	var endpoints []string
	for i := range machines.Items {
		m := &machines.Items[i]
		cond := apimeta.FindStatusCondition(m.Status.Conditions, clusterv1.MachineUpToDateCondition)
		if cond == nil || cond.Status != "True" {
			continue
		}
		for _, addr := range m.Status.Addresses {
			if addr.Type == clusterv1.MachineInternalIP && addr.Address != "" {
				endpoints = append(endpoints, addr.Address)
			}
		}
	}
	if len(endpoints) == 0 {
		return nil, "no UpToDate control-plane machine with an internal address"
	}
	return endpoints, ""
}

func (r *Reconciler) gateBlocked(tcp *unstructured.Unstructured, blockers string) {
	gateBlockedMetric.WithLabelValues("convergence").Inc()
	r.Recorder.Eventf(tcp, nil, "Normal", "GateBlocked", "GateBlocked", "sync gated: %s", blockers)
}

// mustClusterName re-derives the cluster name after it was validated in Reconcile.
func mustClusterName(tcp *unstructured.Unstructured) string {
	name, _ := ClusterNameFor(tcp)
	return name
}
