package variables

import (
	"testing"

	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/api/v1alpha1"
)

func TestClusterConfigSchemaFromCRD(t *testing.T) {
	schema := MustSchemaFromCRDYAML(v1alpha1.TalosClusterConfigCRD)

	if schema.OpenAPIV3Schema.Type != "object" {
		t.Fatalf("root type = %q, want object", schema.OpenAPIV3Schema.Type)
	}
	talos, ok := schema.OpenAPIV3Schema.Properties["talos"]
	if !ok {
		t.Fatal("schema is missing the talos property")
	}
	version, ok := talos.Properties["version"]
	if !ok {
		t.Fatal("schema is missing talos.version")
	}
	if version.Pattern == "" {
		t.Error("talos.version lost its kubebuilder validation pattern")
	}

	endpoint, ok := schema.OpenAPIV3Schema.Properties["controlPlaneEndpoint"]
	if !ok {
		t.Fatal("schema is missing controlPlaneEndpoint")
	}
	if got := endpoint.Required; len(got) != 1 || got[0] != "host" {
		t.Errorf("controlPlaneEndpoint.required = %v, want [host]", got)
	}
	port := endpoint.Properties["port"]
	if port.Default == nil || string(port.Default.Raw) != "6443" {
		t.Errorf("port default = %v, want 6443", port.Default)
	}
	// The variable root's own required list must carry controlPlaneEndpoint.
	found := false
	for _, r := range schema.OpenAPIV3Schema.Required {
		if r == "controlPlaneEndpoint" {
			found = true
		}
	}
	if !found {
		t.Errorf("root required = %v, want to include controlPlaneEndpoint", schema.OpenAPIV3Schema.Required)
	}
}

func TestWorkerConfigSchemaFromCRD(t *testing.T) {
	schema := MustSchemaFromCRDYAML(v1alpha1.TalosWorkerNodeConfigCRD)
	if _, ok := schema.OpenAPIV3Schema.Properties["talos"]; !ok {
		t.Fatal("worker schema is missing the talos property")
	}
	// Metadata/apiVersion/kind are CRD envelope, not variable surface.
	if _, ok := schema.OpenAPIV3Schema.Properties["metadata"]; ok {
		t.Error("CRD envelope leaked into the variable schema")
	}
}

func TestSchemaFromCRDYAMLRejectsGarbage(t *testing.T) {
	if _, err := SchemaFromCRDYAML([]byte("not: a crd")); err == nil {
		t.Fatal("SchemaFromCRDYAML(garbage) = nil error")
	}
}
