// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package-file provenance: re-derived from siderolabs/talos v1.13.5
// pkg/cluster/kubernetes/talos_managed.go (PerformManifestsSync/syncManifestsSSA),
// ported onto github.com/siderolabs/go-kubernetes so this repo never imports the
// full talos module (fork-replaced containerd, cloud SDK weight — see the
// Dependency strategy decision in docs/talos-upgrade-coordinator.md).

package upgrade

import (
	"context"
	"fmt"
	"time"

	"github.com/blang/semver/v4"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/siderolabs/go-kubernetes/kubernetes/manifests"
	"github.com/siderolabs/go-kubernetes/kubernetes/ssa"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

// ssaMinimumTalosVersion is the version from which talosctl switched manifest
// sync to SSA-with-inventory; the fleet is beyond it, and the coordinator does
// not carry the legacy client-side path.
var ssaMinimumTalosVersion = semver.MustParse("1.13.0")

// SyncOptions mirror talosctl's upgrade-k8s manifest flags.
type SyncOptions struct {
	// DryRun diffs and reports without writing anything (and the caller must not
	// checkpoint).
	DryRun bool
	// Force recreates objects with immutable field changes.
	Force bool
	// NoPrune disables removal of objects Talos no longer renders.
	NoPrune bool
	// ReconcileTimeout bounds the post-apply wait for cluster-scoped resources.
	ReconcileTimeout time.Duration
	// Log receives one line per change, slog-style (msg, key/value pairs).
	Log func(msg string, args ...any)
}

// SyncReport counts what the apply (or diff) did.
type SyncReport struct {
	Created    int
	Configured int
	Unchanged  int
	Pruned     int
	// Diffs carries the per-object dry-run diff summaries (empty on real applies).
	Diffs []string
}

// SyncBootstrapManifests fetches the bootstrap manifests Talos has rendered from
// the machine configs (COSI k8s.Manifest resources in the controlplane namespace)
// and server-side-applies them to the workload cluster under talosctl's own field
// manager and inventory, so the coordinator and a human `talosctl upgrade-k8s`
// converge on identical ownership and pruning state.
func SyncBootstrapManifests(ctx context.Context, cosi state.State, restCfg *rest.Config, talosVersion semver.Version, o SyncOptions) (SyncReport, error) {
	var report SyncReport

	if talosVersion.LT(ssaMinimumTalosVersion) {
		return report, fmt.Errorf("observed Talos %s predates the v%s SSA sync contract; run talosctl upgrade-k8s manually", talosVersion, ssaMinimumTalosVersion)
	}

	objects, err := manifests.GetBootstrapManifests(ctx, cosi, nil)
	if err != nil {
		return report, fmt.Errorf("fetching bootstrap manifests: %w", err)
	}
	if len(objects) == 0 {
		return report, fmt.Errorf("no bootstrap manifests found in the node's COSI state")
	}

	mgr, err := ssa.NewManager(ctx, restCfg, constants.KubernetesFieldManagerName,
		constants.KubernetesInventoryNamespace, constants.KubernetesBootstrapManifestsInventoryName)
	if err != nil {
		return report, fmt.Errorf("building SSA manager: %w", err)
	}

	if o.DryRun {
		return diffManifests(ctx, mgr, objects, o)
	}
	return applyManifests(ctx, mgr, objects, o)
}

func diffManifests(ctx context.Context, mgr *ssa.Manager, objects []*unstructured.Unstructured, o SyncOptions) (SyncReport, error) {
	var report SyncReport
	results, err := mgr.Diff(ctx, objects, ssa.DiffOptions{
		InventoryPolicy: ssa.InventoryPolicyAdoptIfNoInventory,
		NoPrune:         o.NoPrune,
		Force:           o.Force,
	})
	if err != nil {
		return report, fmt.Errorf("diffing bootstrap manifests: %w", err)
	}
	for _, r := range results {
		report.Diffs = append(report.Diffs, fmt.Sprintf("%s %s", r.Action, r.ObjMetadata.String()))
		if o.Log != nil {
			o.Log("manifest diff", "action", string(r.Action), "object", r.ObjMetadata.String())
		}
	}
	return report, nil
}

func applyManifests(ctx context.Context, mgr *ssa.Manager, objects []*unstructured.Unstructured, o SyncOptions) (SyncReport, error) {
	var report SyncReport
	opts := ssa.ApplyOptions{
		InventoryPolicy: ssa.InventoryPolicyAdoptIfNoInventory,
		NoPrune:         o.NoPrune,
		Force:           o.Force,
	}
	opts.WaitTimeout = o.ReconcileTimeout
	changes, err := mgr.Apply(ctx, objects, opts)
	for _, change := range changes {
		switch change.Action {
		case ssa.CreatedAction:
			report.Created++
		case ssa.ConfiguredAction:
			report.Configured++
		case ssa.UnchangedAction:
			report.Unchanged++
		case ssa.DeletedAction:
			report.Pruned++
		}
		if o.Log != nil {
			o.Log("manifest applied", "action", string(change.Action), "object", change.ObjMetadata.String())
		}
	}
	if err != nil {
		return report, fmt.Errorf("applying bootstrap manifests: %w", err)
	}
	return report, nil
}
