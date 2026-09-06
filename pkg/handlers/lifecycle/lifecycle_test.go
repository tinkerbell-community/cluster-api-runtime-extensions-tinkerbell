package lifecycle

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// callLog collects sub-handler invocations; the fan-out is genuinely parallel,
// so the test's own record must be synchronized.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, entry)
}

func (l *callLog) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

func subHandler(name string, retry int32, err error, log *callLog) SubHandler {
	return SubHandler{
		Name: name,
		OnEvent: func(_ context.Context, cluster client.ObjectKey) (int32, error) {
			log.add(name + ":" + cluster.Name)
			return retry, err
		},
	}
}

func upgradeCluster() clusterv1.Cluster {
	c := clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "tink"}}
	return c
}

func TestBeforeClusterUpgradeAggregates(t *testing.T) {
	calls := &callLog{}
	d := &Dispatcher{OnBeforeClusterUpgrade: []SubHandler{
		subHandler("a", 0, nil, calls),
		subHandler("b", 30, nil, calls),
		subHandler("c", 10, nil, calls),
	}}
	req := &runtimehooksv1.BeforeClusterUpgradeRequest{Cluster: upgradeCluster()}
	resp := &runtimehooksv1.BeforeClusterUpgradeResponse{}
	d.BeforeClusterUpgrade(context.Background(), req, resp)

	if resp.Status != runtimehooksv1.ResponseStatusSuccess {
		t.Fatalf("status = %s (%s)", resp.Status, resp.Message)
	}
	if resp.RetryAfterSeconds != 10 {
		t.Errorf("RetryAfterSeconds = %d, want lowest non-zero 10", resp.RetryAfterSeconds)
	}
	if calls.len() != 3 {
		t.Errorf("calls = %v, want all three sub-handlers", calls.calls)
	}
}

// TestSubHandlerErrorIsFailOpen: this extension's lifecycle participation is
// additive — a sub-handler failure surfaces in the message but never blocks the
// topology controller's upgrade.
func TestSubHandlerErrorIsFailOpen(t *testing.T) {
	calls := &callLog{}
	d := &Dispatcher{OnAfterControlPlaneUpgrade: []SubHandler{
		subHandler("boom", 0, errors.New("sync exploded"), calls),
		subHandler("ok", 0, nil, calls),
	}}
	req := &runtimehooksv1.AfterControlPlaneUpgradeRequest{Cluster: upgradeCluster()}
	resp := &runtimehooksv1.AfterControlPlaneUpgradeResponse{}
	d.AfterControlPlaneUpgrade(context.Background(), req, resp)

	if resp.Status != runtimehooksv1.ResponseStatusSuccess {
		t.Fatalf("status = %s, want fail-open Success", resp.Status)
	}
	if resp.RetryAfterSeconds != 0 {
		t.Errorf("RetryAfterSeconds = %d, want 0", resp.RetryAfterSeconds)
	}
	if !strings.Contains(resp.Message, "boom") {
		t.Errorf("message %q does not surface the failed sub-handler", resp.Message)
	}
	if calls.len() != 2 {
		t.Errorf("calls = %v, want the error not to stop the fan-out", calls.calls)
	}
}

func TestNoSubHandlersSucceedsImmediately(t *testing.T) {
	d := &Dispatcher{}
	resp := &runtimehooksv1.BeforeClusterUpgradeResponse{}
	d.BeforeClusterUpgrade(context.Background(), &runtimehooksv1.BeforeClusterUpgradeRequest{Cluster: upgradeCluster()}, resp)
	if resp.Status != runtimehooksv1.ResponseStatusSuccess || resp.RetryAfterSeconds != 0 {
		t.Fatalf("empty dispatcher: %s retry=%d", resp.Status, resp.RetryAfterSeconds)
	}
}
