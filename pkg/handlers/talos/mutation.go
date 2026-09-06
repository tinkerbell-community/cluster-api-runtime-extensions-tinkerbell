package talos

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
	"sigs.k8s.io/cluster-api/exp/runtime/topologymutation"
)

// Handler names are versioned from day one (spec §4.4): patch-output drift for
// unchanged inputs folds into the bootstrap provider's in-place config hash and
// would trigger a fleet-wide rollout. A behavior change ships as a NEW v2
// handler registered side by side; these v1 names stay byte-stable.
const (
	// ClusterPatchHandlerName mutates TalosControlPlaneTemplate and
	// TinkerbellClusterTemplate.
	ClusterPatchHandlerName = "talostinkerbellclusterconfigpatchv1-gp"
	// WorkerPatchHandlerName mutates TalosConfigTemplate (worker bootstrap).
	WorkerPatchHandlerName = "talostinkerbellworkerconfigpatchv1-gp"
)

// Template GVKs mutated by the handlers. Mutation is unstructured on purpose
// (spec §9 Q8's alternative path): the walker computes an RFC6902 patch from
// the raw original against the mutated object, so unstructured mutation is
// field-preserving by construction — a partially-vendored typed decode would
// silently DROP any field the local type didn't model, emitting destructive
// remove ops. Byte-preservation beats type ergonomics here.
var (
	talosControlPlaneTemplateGVK = schema.GroupVersionKind{Group: "controlplane.cluster.x-k8s.io", Version: "v1beta1", Kind: "TalosControlPlaneTemplate"}
	tinkerbellClusterTemplateGVK = schema.GroupVersionKind{Group: "infrastructure.cluster.x-k8s.io", Version: "v1beta2", Kind: "TinkerbellClusterTemplate"}
	talosConfigTemplateGVK       = schema.GroupVersionKind{Group: "bootstrap.cluster.x-k8s.io", Version: "v1beta1", Kind: "TalosConfigTemplate"}
)

// injectableVersion matches the Talos versions the mutators inject verbatim: a
// full pin or a bare minor — both valid bootstrap-provider talosVersion
// contracts AND resolver inputs. "latest" and empty are deliberately NOT
// injected: unset/latest policy (contract-pinning) is the resolver's domain,
// and the bootstrap provider requires a concrete version contract.
var injectableVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+(\.[0-9]+([-+].+)?)?$`)

// clusterConfigVariable mirrors api/v1alpha1.TalosClusterConfigSpec for
// variable decoding.
type clusterConfigVariable struct {
	Talos *struct {
		Version string `json:"version,omitempty"`
	} `json:"talos,omitempty"`
	ControlPlaneEndpoint struct {
		Host string `json:"host,omitempty"`
		Port int64  `json:"port,omitempty"`
	} `json:"controlPlaneEndpoint,omitempty"`
}

func decodeVariable[T any](variables map[string]apiextensionsv1.JSON, name string) (*T, error) {
	raw, found := variables[name]
	if !found {
		return nil, nil //nolint:nilnil // absent variable is a valid state, not an error
	}
	out := new(T)
	if err := json.Unmarshal(raw.Raw, out); err != nil {
		return nil, fmt.Errorf("decoding %q variable: %w", name, err)
	}
	return out, nil
}

func (v *clusterConfigVariable) version() string {
	if v == nil || v.Talos == nil {
		return ""
	}
	return v.Talos.Version
}

// ClusterPatchHandler serves talostinkerbellclusterconfigpatchv1-gp: the Talos
// version half of the upgrade path (Strategy B injects ONLY the version — the
// per-machine installer image rides the resolver's Hardware annotation) and the
// controlPlaneEndpoint into TinkerbellClusterTemplate.
type ClusterPatchHandler struct{}

// GeneratePatches implements the topology mutation hook.
func (h *ClusterPatchHandler) GeneratePatches(ctx context.Context, req *runtimehooksv1.GeneratePatchesRequest, resp *runtimehooksv1.GeneratePatchesResponse) {
	topologymutation.WalkTemplates(ctx, unstructured.UnstructuredJSONScheme, req, resp,
		func(_ context.Context, obj runtime.Object, variables map[string]apiextensionsv1.JSON, _ runtimehooksv1.HolderReference) error {
			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return nil
			}
			cc, err := decodeVariable[clusterConfigVariable](variables, ClusterConfigVariableName)
			if err != nil {
				return err
			}
			if cc == nil {
				return nil
			}
			switch u.GroupVersionKind() {
			case talosControlPlaneTemplateGVK:
				return injectVersion(u, cc.version(), "spec", "template", "spec", "controlPlaneConfig", "controlplane", "talosVersion")
			case tinkerbellClusterTemplateGVK:
				return injectEndpoint(u, cc)
			default:
				return nil
			}
		})
}

// WorkerPatchHandler serves talostinkerbellworkerconfigpatchv1-gp: the same
// version mutator re-parameterized against the worker bootstrap template and
// the workerConfig variable path (constructor parameterization, never forked
// mutator logic — spec §4.5).
type WorkerPatchHandler struct{}

// GeneratePatches implements the topology mutation hook.
func (h *WorkerPatchHandler) GeneratePatches(ctx context.Context, req *runtimehooksv1.GeneratePatchesRequest, resp *runtimehooksv1.GeneratePatchesResponse) {
	topologymutation.WalkTemplates(ctx, unstructured.UnstructuredJSONScheme, req, resp,
		func(_ context.Context, obj runtime.Object, variables map[string]apiextensionsv1.JSON, _ runtimehooksv1.HolderReference) error {
			u, ok := obj.(*unstructured.Unstructured)
			if !ok || u.GroupVersionKind() != talosConfigTemplateGVK {
				return nil
			}
			wc, err := decodeVariable[clusterConfigVariable](variables, WorkerConfigVariableName)
			if err != nil {
				return err
			}
			version := wc.version()
			if version == "" {
				cc, err := decodeVariable[clusterConfigVariable](variables, ClusterConfigVariableName)
				if err != nil {
					return err
				}
				version = cc.version()
			}
			return injectVersion(u, version, "spec", "template", "spec", "talosVersion")
		})
}

func injectVersion(u *unstructured.Unstructured, version string, path ...string) error {
	if !injectableVersion.MatchString(version) {
		return nil
	}
	if err := unstructured.SetNestedField(u.Object, version, path...); err != nil {
		return fmt.Errorf("injecting talosVersion: %w", err)
	}
	return nil
}

func injectEndpoint(u *unstructured.Unstructured, cc *clusterConfigVariable) error {
	if cc.ControlPlaneEndpoint.Host == "" {
		return nil
	}
	endpoint := map[string]any{"host": cc.ControlPlaneEndpoint.Host}
	if cc.ControlPlaneEndpoint.Port != 0 {
		endpoint["port"] = cc.ControlPlaneEndpoint.Port
	}
	if err := unstructured.SetNestedMap(u.Object, endpoint, "spec", "template", "spec", "controlPlaneEndpoint"); err != nil {
		return fmt.Errorf("injecting controlPlaneEndpoint: %w", err)
	}
	return nil
}
