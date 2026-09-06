package talos

import (
	"context"
	"encoding/json"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
)

func patchRequest(t *testing.T, clusterConfig, workerConfig string, items ...runtimehooksv1.GeneratePatchesRequestItem) *runtimehooksv1.GeneratePatchesRequest {
	t.Helper()
	req := &runtimehooksv1.GeneratePatchesRequest{Items: items}
	if clusterConfig != "" {
		req.Variables = append(req.Variables, runtimehooksv1.Variable{
			Name: ClusterConfigVariableName, Value: apiextensionsv1.JSON{Raw: []byte(clusterConfig)},
		})
	}
	if workerConfig != "" {
		req.Variables = append(req.Variables, runtimehooksv1.Variable{
			Name: WorkerConfigVariableName, Value: apiextensionsv1.JSON{Raw: []byte(workerConfig)},
		})
	}
	return req
}

func item(t *testing.T, uid, rawObject string) runtimehooksv1.GeneratePatchesRequestItem {
	t.Helper()
	return runtimehooksv1.GeneratePatchesRequestItem{
		UID:             types.UID(uid),
		Object:          runtime.RawExtension{Raw: []byte(rawObject)},
		HolderReference: runtimehooksv1.HolderReference{APIVersion: "cluster.x-k8s.io/v1beta2", Kind: "Cluster", Name: "wl", Namespace: "tink"},
	}
}

const (
	tcpTemplateRaw = `{"apiVersion":"controlplane.cluster.x-k8s.io/v1beta1","kind":"TalosControlPlaneTemplate",` +
		`"metadata":{"name":"cc-cp"},"spec":{"template":{"spec":{"controlPlaneConfig":{"controlplane":{"generateType":"controlplane"}},` +
		`"rolloutStrategy":{"type":"RollingUpdate","rollingUpdate":{"maxSurge":0}}}}}}`
	tinkClusterTemplateRaw = `{"apiVersion":"infrastructure.cluster.x-k8s.io/v1beta2","kind":"TinkerbellClusterTemplate",` +
		`"metadata":{"name":"cc-cluster"},"spec":{"template":{"spec":{}}}}`
	talosConfigTemplateRaw = `{"apiVersion":"bootstrap.cluster.x-k8s.io/v1beta1","kind":"TalosConfigTemplate",` +
		`"metadata":{"name":"cc-worker"},"spec":{"template":{"spec":{"generateType":"worker"}}}}`
)

// decodePatches returns uid -> the applied-patch operations for inspection.
func decodePatches(t *testing.T, resp *runtimehooksv1.GeneratePatchesResponse) map[string][]map[string]any {
	t.Helper()
	out := map[string][]map[string]any{}
	for _, i := range resp.Items {
		var ops []map[string]any
		if err := json.Unmarshal(i.Patch, &ops); err != nil {
			t.Fatalf("patch for %s is not a JSON patch: %v (%s)", i.UID, err, i.Patch)
		}
		out[string(i.UID)] = ops
	}
	return out
}

func TestClusterPatchInjectsTalosVersionAndEndpoint(t *testing.T) {
	resp := &runtimehooksv1.GeneratePatchesResponse{}
	req := patchRequest(t,
		`{"talos":{"version":"v1.13.9"},"controlPlaneEndpoint":{"host":"10.0.0.10","port":6443}}`, "",
		item(t, "tcp", tcpTemplateRaw),
		item(t, "cluster", tinkClusterTemplateRaw),
	)
	(&ClusterPatchHandler{}).GeneratePatches(context.Background(), req, resp)
	if resp.Status != runtimehooksv1.ResponseStatusSuccess {
		t.Fatalf("status = %s (%s)", resp.Status, resp.Message)
	}
	patches := decodePatches(t, resp)

	tcpOps := patches["tcp"]
	if len(tcpOps) != 1 || tcpOps[0]["op"] != "add" ||
		tcpOps[0]["path"] != "/spec/template/spec/controlPlaneConfig/controlplane/talosVersion" ||
		tcpOps[0]["value"] != "v1.13.9" {
		t.Errorf("TalosControlPlaneTemplate patch = %v, want single add of talosVersion", tcpOps)
	}

	clusterOps := patches["cluster"]
	if len(clusterOps) != 1 || clusterOps[0]["path"] != "/spec/template/spec/controlPlaneEndpoint" {
		t.Fatalf("TinkerbellClusterTemplate patch = %v, want controlPlaneEndpoint add", clusterOps)
	}
	endpoint, _ := clusterOps[0]["value"].(map[string]any)
	if endpoint["host"] != "10.0.0.10" || endpoint["port"] != float64(6443) {
		t.Errorf("endpoint value = %v", endpoint)
	}
}

// TestClusterPatchMinimalDiff is the §4.4 guard: for unchanged inputs the
// emitted patch must be empty — a spurious op would fold into the in-place
// config hash and roll the fleet.
func TestClusterPatchMinimalDiff(t *testing.T) {
	resp := &runtimehooksv1.GeneratePatchesResponse{}
	req := patchRequest(t, `{"controlPlaneEndpoint":{"host":"","port":0}}`, "",
		item(t, "tcp", tcpTemplateRaw))
	(&ClusterPatchHandler{}).GeneratePatches(context.Background(), req, resp)
	if resp.Status != runtimehooksv1.ResponseStatusSuccess {
		t.Fatalf("status = %s (%s)", resp.Status, resp.Message)
	}
	for uid, ops := range decodePatches(t, resp) {
		if len(ops) != 0 {
			t.Errorf("no-variable request emitted ops for %s: %v", uid, ops)
		}
	}
}

func TestClusterPatchSkipsNonVersionValues(t *testing.T) {
	for _, version := range []string{"latest", ""} {
		resp := &runtimehooksv1.GeneratePatchesResponse{}
		req := patchRequest(t, `{"talos":{"version":"`+version+`"},"controlPlaneEndpoint":{"host":"h"}}`, "",
			item(t, "tcp", tcpTemplateRaw))
		(&ClusterPatchHandler{}).GeneratePatches(context.Background(), req, resp)
		if resp.Status != runtimehooksv1.ResponseStatusSuccess {
			t.Fatalf("status = %s (%s)", resp.Status, resp.Message)
		}
		for _, ops := range decodePatches(t, resp) {
			for _, op := range ops {
				if op["path"] == "/spec/template/spec/controlPlaneConfig/controlplane/talosVersion" {
					t.Errorf("version %q must not be injected (the resolver owns unset/latest policy)", version)
				}
			}
		}
	}
}

func TestWorkerPatchInjectsVersionWithOverride(t *testing.T) {
	tests := []struct {
		name          string
		clusterConfig string
		workerConfig  string
		want          string
	}{
		{"cluster version flows to workers", `{"talos":{"version":"v1.13.9"},"controlPlaneEndpoint":{"host":"h"}}`, "", "v1.13.9"},
		{"worker override wins", `{"talos":{"version":"v1.13.9"},"controlPlaneEndpoint":{"host":"h"}}`, `{"talos":{"version":"v1.13.8"}}`, "v1.13.8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &runtimehooksv1.GeneratePatchesResponse{}
			req := patchRequest(t, tt.clusterConfig, tt.workerConfig, item(t, "cfg", talosConfigTemplateRaw))
			(&WorkerPatchHandler{}).GeneratePatches(context.Background(), req, resp)
			if resp.Status != runtimehooksv1.ResponseStatusSuccess {
				t.Fatalf("status = %s (%s)", resp.Status, resp.Message)
			}
			ops := decodePatches(t, resp)["cfg"]
			if len(ops) != 1 || ops[0]["path"] != "/spec/template/spec/talosVersion" || ops[0]["value"] != tt.want {
				t.Errorf("TalosConfigTemplate patch = %v, want talosVersion=%s", ops, tt.want)
			}
		})
	}
}

// TestPatchHandlersIgnoreForeignTemplates: a template kind a handler does not
// own passes through untouched (empty patch), never an error.
func TestPatchHandlersIgnoreForeignTemplates(t *testing.T) {
	resp := &runtimehooksv1.GeneratePatchesResponse{}
	req := patchRequest(t, `{"talos":{"version":"v1.13.9"},"controlPlaneEndpoint":{"host":"h"}}`, "",
		item(t, "cfg", talosConfigTemplateRaw)) // worker template sent to the CLUSTER handler
	(&ClusterPatchHandler{}).GeneratePatches(context.Background(), req, resp)
	if resp.Status != runtimehooksv1.ResponseStatusSuccess {
		t.Fatalf("status = %s (%s)", resp.Status, resp.Message)
	}
	if ops := decodePatches(t, resp)["cfg"]; len(ops) != 0 {
		t.Errorf("cluster handler mutated a worker template: %v", ops)
	}
}
