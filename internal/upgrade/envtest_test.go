//go:build envtest

package upgrade

// The SSA sync path against a real API server: adoption into the talos inventory,
// the foreign `talos` field manager, prune-on-removal, and dry-run writing
// nothing. The COSI state.State seam is the injection point (an in-memory state
// stands in for a Talos node), so no Talos gRPC server is needed — exactly the
// test strategy in docs/talos-upgrade-coordinator.md. Run: make test-envtest

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/blang/semver/v4"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/cosi-project/runtime/pkg/state/impl/inmem"
	"github.com/cosi-project/runtime/pkg/state/impl/namespaced"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"github.com/siderolabs/talos/pkg/machinery/resources/k8s"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var envCfg *rest.Config

func TestMain(m *testing.M) {
	testEnv := &envtest.Environment{}
	cfg, err := testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting envtest: %v\n", err)
		os.Exit(1)
	}
	envCfg = cfg
	code := m.Run()
	_ = testEnv.Stop()
	os.Exit(code)
}

// seedCOSI builds an in-memory COSI state carrying one k8s.Manifest whose items
// are the given ConfigMap names (in a dedicated namespace so tests are isolated).
func seedCOSI(t *testing.T, ns string, configMaps ...string) state.State {
	t.Helper()
	st := state.WrapCore(namespaced.NewState(inmem.Build))

	manifest := k8s.NewManifest(k8s.ControlPlaneNamespaceName, "10-test-manifests")
	items := make([]k8s.SingleManifest, 0, len(configMaps)+1)
	items = append(items, k8s.SingleManifest{Object: map[string]any{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]any{"name": ns},
	}})
	for _, name := range configMaps {
		items = append(items, k8s.SingleManifest{Object: map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": name, "namespace": ns},
			"data":     map[string]any{"talos": "rendered"},
		}})
	}
	manifest.TypedSpec().Items = items
	if err := st.Create(context.Background(), manifest); err != nil {
		t.Fatalf("seeding COSI state: %v", err)
	}
	return st
}

func workloadClient(t *testing.T) kubernetes.Interface {
	t.Helper()
	c, err := kubernetes.NewForConfig(envCfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEnvtestSyncAppliesAdoptsAndPrunes(t *testing.T) {
	ctx := context.Background()
	talosV := semver.MustParse("1.13.9")
	c := workloadClient(t)

	// First sync: two manifests, adopted into the talos inventory.
	report, err := SyncBootstrapManifests(ctx, seedCOSI(t, "sync-a", "cm-one", "cm-two"), envCfg, talosV, SyncOptions{})
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if report.Created == 0 {
		t.Errorf("first sync report = %+v, want created objects", report)
	}

	cm, err := c.CoreV1().ConfigMaps("sync-a").Get(ctx, "cm-one", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("cm-one not applied: %v", err)
	}
	assertTalosManager(t, cm)

	inv, err := c.CoreV1().ConfigMaps(constants.KubernetesInventoryNamespace).
		Get(ctx, constants.KubernetesBootstrapManifestsInventoryName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("inventory ConfigMap missing: %v", err)
	}
	if len(inv.Data) == 0 {
		t.Error("inventory ConfigMap has no entries")
	}

	// Second sync with cm-two gone from the rendered set: prune removes it.
	report, err = SyncBootstrapManifests(ctx, seedCOSI(t, "sync-a", "cm-one"), envCfg, talosV, SyncOptions{})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if report.Pruned == 0 {
		t.Errorf("second sync report = %+v, want pruned objects", report)
	}
	if _, err := c.CoreV1().ConfigMaps("sync-a").Get(ctx, "cm-two", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("cm-two after prune: err = %v, want NotFound", err)
	}
	if _, err := c.CoreV1().ConfigMaps("sync-a").Get(ctx, "cm-one", metav1.GetOptions{}); err != nil {
		t.Errorf("cm-one must survive the prune: %v", err)
	}
}

func TestEnvtestSyncDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	report, err := SyncBootstrapManifests(ctx, seedCOSI(t, "sync-dry", "cm-dry"), envCfg,
		semver.MustParse("1.13.9"), SyncOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry-run sync: %v", err)
	}
	if len(report.Diffs) == 0 {
		t.Errorf("dry-run report = %+v, want pending diffs", report)
	}
	c := workloadClient(t)
	if _, err := c.CoreV1().Namespaces().Get(ctx, "sync-dry", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("dry-run created the namespace: err = %v, want NotFound", err)
	}
}

func TestEnvtestSyncRefusesPreSSATalos(t *testing.T) {
	_, err := SyncBootstrapManifests(context.Background(), seedCOSI(t, "sync-old", "cm"), envCfg,
		semver.MustParse("1.12.4"), SyncOptions{})
	if err == nil {
		t.Fatal("sync accepted a pre-SSA Talos version")
	}
}

func assertTalosManager(t *testing.T, cm *corev1.ConfigMap) {
	t.Helper()
	for _, mf := range cm.GetManagedFields() {
		if mf.Manager == constants.KubernetesFieldManagerName {
			return
		}
	}
	t.Errorf("ConfigMap %s/%s has no %q field manager entry", cm.Namespace, cm.Name, constants.KubernetesFieldManagerName)
}
