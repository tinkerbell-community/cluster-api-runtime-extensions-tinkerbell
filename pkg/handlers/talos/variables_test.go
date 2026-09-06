package talos

import (
	"context"
	"testing"

	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
)

func TestDiscoverVariables(t *testing.T) {
	resp := &runtimehooksv1.DiscoverVariablesResponse{}
	(&VariablesHandler{}).DiscoverVariables(context.Background(), &runtimehooksv1.DiscoverVariablesRequest{}, resp)

	if resp.Status != runtimehooksv1.ResponseStatusSuccess {
		t.Fatalf("status = %q, want success", resp.Status)
	}
	if len(resp.Variables) != 2 {
		t.Fatalf("variables = %d, want clusterConfig + workerConfig", len(resp.Variables))
	}
	cc := resp.Variables[0]
	if cc.Name != ClusterConfigVariableName || cc.Required == nil || !*cc.Required {
		t.Errorf("first variable = %+v, want required clusterConfig", cc)
	}
	if _, ok := cc.Schema.OpenAPIV3Schema.Properties["controlPlaneEndpoint"]; !ok {
		t.Error("clusterConfig schema is missing controlPlaneEndpoint")
	}
	wc := resp.Variables[1]
	if wc.Name != WorkerConfigVariableName || wc.Required == nil || *wc.Required {
		t.Errorf("second variable = %+v, want optional workerConfig", wc)
	}
}
