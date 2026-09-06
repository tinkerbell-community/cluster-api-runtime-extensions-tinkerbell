package main

import (
	"fmt"

	runtimecatalog "sigs.k8s.io/cluster-api/exp/runtime/catalog"
	runtimeserver "sigs.k8s.io/cluster-api/exp/runtime/server"

	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"

	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/pkg/handlers/talos"
)

// runtimeCatalog registers the Runtime SDK hook types this server can speak.
func runtimeCatalog() *runtimecatalog.Catalog {
	catalog := runtimecatalog.New()
	_ = runtimehooksv1.AddToCatalog(catalog)
	return catalog
}

// runtimeExtensionHandlers is the complete, closed list of Runtime SDK
// handlers this binary registers. THE ONE HARD INVARIANT (spec §2.4): none of
// them may be an in-place hook — CanUpdateMachine/CanUpdateMachineSet/
// UpdateMachine belong exclusively to the bootstrap provider's extension, and
// core CAPI hard-fails a second UpdateMachine registration, breaking in-place
// updates for the whole management cluster. TestNoInPlaceHooksRegistered
// enforces this at build time; grow this list only through it.
func runtimeExtensionHandlers() []runtimeserver.ExtensionHandler {
	return []runtimeserver.ExtensionHandler{
		{
			Hook:        runtimehooksv1.DiscoverVariables,
			Name:        talos.DiscoverVariablesHandlerName,
			HandlerFunc: (&talos.VariablesHandler{}).DiscoverVariables,
		},
		{
			Hook:        runtimehooksv1.GeneratePatches,
			Name:        talos.ClusterPatchHandlerName,
			HandlerFunc: (&talos.ClusterPatchHandler{}).GeneratePatches,
		},
		{
			Hook:        runtimehooksv1.GeneratePatches,
			Name:        talos.WorkerPatchHandlerName,
			HandlerFunc: (&talos.WorkerPatchHandler{}).GeneratePatches,
		},
	}
}

// registerRuntimeHooks adds every handler to the runtime server.
func registerRuntimeHooks(srv *runtimeserver.Server) error {
	for _, handler := range runtimeExtensionHandlers() {
		if err := srv.AddExtensionHandler(handler); err != nil {
			return fmt.Errorf("registering runtime hook handler %q: %w", handler.Name, err)
		}
	}
	return nil
}
