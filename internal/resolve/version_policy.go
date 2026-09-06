package resolve

import (
	"context"
	"regexp"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// fullTalosVersion matches a complete Talos version (v1.14.0 or v1.14.0-rc.1). A bare minor
// (v1.13) is a config contract, not an installable OS version, so it is deliberately rejected.
var fullTalosVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.\-]+)?$`)

// VersionPolicy turns a spec-declared Talos version (full / bare-minor / unset) into the
// concrete patch the machine installs and upgrades to. It ports CAPT's policy 1:1; only the
// contract-pin carrier changes (the resolver stamps the pin on Hardware, not TinkerbellMachine).
type VersionPolicy struct {
	Versions *VersionResolver
}

// Resolve returns the concrete version to use plus a contract minor to stamp (empty = none).
//
//   - A full pin (v1.13.9) is used exactly, never bumped.
//   - A bare minor (v1.13) tracks the newest GA patch in that minor.
//   - Unset / "latest" pins the newest GA minor on first resolution of an unprovisioned machine
//     (returned as newPin for the caller to stamp); an already-pinned machine tracks its pin.
//   - A provisioned, unpinned machine is left unresolved (""), never pinned, so the resolver can
//     never ask CABPT to skip a minor.
//
// An empty version means "not knowable" — the caller writes no operating_system.
func (p VersionPolicy) Resolve(ctx context.Context, raw string, provisioned bool, existingPin string) (version, newPin string, err error) {
	if fullTalosVersion.MatchString(raw) {
		return raw, "", nil
	}
	if p.Versions == nil {
		return "", "", nil
	}
	floor := raw
	if floor == "" || floor == "latest" {
		floor, newPin, err = p.contractMinor(ctx, provisioned, existingPin)
		if err != nil {
			return "", "", err
		}
	}
	resolved, err := p.Versions.LatestPatch(ctx, floor)
	if err != nil {
		return "", "", err
	}
	return resolved, newPin, nil
}

// contractMinor returns the minor an unpinned machine tracks, plus a newPin when a fresh pin
// is established. An already-pinned machine keeps its pin (newPin empty); a provisioned machine
// is never pinned (its running minor is unknown here).
func (p VersionPolicy) contractMinor(ctx context.Context, provisioned bool, existingPin string) (floor, newPin string, err error) {
	if existingPin != "" {
		return existingPin, "", nil
	}
	if provisioned {
		return "", "", nil
	}
	minor, err := p.Versions.LatestMinor(ctx)
	if err != nil {
		return "", "", err
	}
	if minor == "" {
		return "", "", nil
	}
	return minor, minor, nil
}

// SpecTalosVersion reads spec.talosVersion from the Machine's bootstrap config, unstructured on
// purpose (no bootstrap-provider import; any provider exposing spec.talosVersion works). An empty
// result means unset or not readable yet. RBAC MUST grant get on talosconfigs — a Forbidden here
// silently disables resolution (§3.4, architecture.md P3).
func SpecTalosVersion(ctx context.Context, c client.Reader, machine *clusterv1.Machine) string {
	if machine == nil || !machine.Spec.Bootstrap.ConfigRef.IsDefined() {
		return ""
	}
	ref := machine.Spec.Bootstrap.ConfigRef
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: ref.APIGroup, Version: "v1beta1", Kind: ref.Kind})

	key := types.NamespacedName{Namespace: machine.Namespace, Name: ref.Name}
	if err := c.Get(ctx, key, obj); err != nil {
		logf.FromContext(ctx).V(1).Info("could not read bootstrap config for Talos version", "error", err.Error())
		return ""
	}
	version, found, err := unstructured.NestedString(obj.Object, "spec", "talosVersion")
	if err != nil || !found {
		return ""
	}
	return version
}

// MachineExtensions reads extra system extensions requested on the TinkerbellMachine.
func MachineExtensions(annotations map[string]string) []string {
	return splitCSV(annotations[ExtensionsAnnotation])
}
