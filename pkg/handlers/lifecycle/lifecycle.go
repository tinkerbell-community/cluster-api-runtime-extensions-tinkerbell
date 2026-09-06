// Package lifecycle serves the cluster lifecycle hooks (spec §6 SP-10): one
// aggregated handler per hook fanning out to sub-handlers in parallel and
// reporting the lowest non-zero retryAfterSeconds. Participation is additive
// and fail-open — a sub-handler error is surfaced in the response message but
// never blocks the topology controller, because nothing this extension does at
// a lifecycle boundary is a correctness precondition for the upgrade itself
// (the upgrade coordinator's own convergence gate still guards every manifest
// sync). The Workflow-CREATE admission gate is deliberately NOT represented
// here: it is load-bearing for install correctness and stays a mandatory
// admission webhook.
package lifecycle

import (
	"context"
	"fmt"
	"strings"
	"sync"

	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// BeforeClusterUpgradeHandlerName / AfterControlPlaneUpgradeHandlerName are
	// the registered handler names; stable for the same reason the -gp/-dv
	// names are.
	BeforeClusterUpgradeHandlerName     = "talostinkerbellbeforeclusterupgrade"
	AfterControlPlaneUpgradeHandlerName = "talostinkerbellaftercontrolplaneupgrade"
)

// SubHandler is one lifecycle participant: today the upgrade coordinator's
// sync nudge; the shape exists so later participants aggregate instead of
// multiplying registered hooks.
type SubHandler struct {
	Name string
	// OnEvent acts for the named cluster and returns a retry-after (0 = done).
	OnEvent func(ctx context.Context, cluster client.ObjectKey) (int32, error)
}

// Dispatcher aggregates sub-handlers per hook.
type Dispatcher struct {
	OnBeforeClusterUpgrade     []SubHandler
	OnAfterControlPlaneUpgrade []SubHandler
}

// BeforeClusterUpgrade fans out before the topology controller begins an
// upgrade — the coordinator uses it to seed/repair the manifest inventory at
// the outgoing version pair while the cluster is still converged.
func (d *Dispatcher) BeforeClusterUpgrade(ctx context.Context, req *runtimehooksv1.BeforeClusterUpgradeRequest, resp *runtimehooksv1.BeforeClusterUpgradeResponse) {
	retry, message := fanOut(ctx, d.OnBeforeClusterUpgrade, client.ObjectKey{Namespace: req.Cluster.Namespace, Name: req.Cluster.Name})
	resp.SetStatus(runtimehooksv1.ResponseStatusSuccess)
	resp.SetMessage(message)
	resp.RetryAfterSeconds = retry
}

// AfterControlPlaneUpgrade fans out once the control plane reaches the target
// version — the earliest useful moment to re-evaluate the coordinator's gate
// without waiting for a Machine watch event.
func (d *Dispatcher) AfterControlPlaneUpgrade(ctx context.Context, req *runtimehooksv1.AfterControlPlaneUpgradeRequest, resp *runtimehooksv1.AfterControlPlaneUpgradeResponse) {
	retry, message := fanOut(ctx, d.OnAfterControlPlaneUpgrade, client.ObjectKey{Namespace: req.Cluster.Namespace, Name: req.Cluster.Name})
	resp.SetStatus(runtimehooksv1.ResponseStatusSuccess)
	resp.SetMessage(message)
	resp.RetryAfterSeconds = retry
}

// fanOut runs every sub-handler concurrently and aggregates: lowest non-zero
// retry wins; errors are collected into the message, never into the status.
func fanOut(ctx context.Context, handlers []SubHandler, cluster client.ObjectKey) (int32, string) {
	if len(handlers) == 0 {
		return 0, ""
	}

	type result struct {
		name  string
		retry int32
		err   error
	}
	results := make([]result, len(handlers))

	var wg sync.WaitGroup
	for i, h := range handlers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			retry, err := h.OnEvent(ctx, cluster)
			results[i] = result{name: h.Name, retry: retry, err: err}
		}()
	}
	wg.Wait()

	var retry int32
	var failures []string
	for _, r := range results {
		if r.err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", r.name, r.err))
			continue
		}
		if r.retry > 0 && (retry == 0 || r.retry < retry) {
			retry = r.retry
		}
	}
	if len(failures) > 0 {
		return retry, "fail-open sub-handler failures: " + strings.Join(failures, "; ")
	}
	return retry, ""
}
