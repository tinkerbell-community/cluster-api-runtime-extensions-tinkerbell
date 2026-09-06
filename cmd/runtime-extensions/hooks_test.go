package main

import (
	"testing"

	runtimecatalog "sigs.k8s.io/cluster-api/exp/runtime/catalog"
	runtimeserver "sigs.k8s.io/cluster-api/exp/runtime/server"

	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
)

// TestNoInPlaceHooksRegistered is the mandatory build/registration guard of
// spec §2.4: this binary must register ZERO in-place hooks. The in-place
// extension surface (CanUpdateMachine/CanUpdateMachineSet/UpdateMachine)
// belongs exclusively to the bootstrap provider, and core CAPI hard-fails a
// second UpdateMachine registration — which would break in-place updates for
// the entire management cluster.
func TestNoInPlaceHooksRegistered(t *testing.T) {
	forbidden := map[string]bool{
		runtimecatalog.HookName(runtimehooksv1.CanUpdateMachine):    true,
		runtimecatalog.HookName(runtimehooksv1.CanUpdateMachineSet): true,
		runtimecatalog.HookName(runtimehooksv1.UpdateMachine):       true,
	}
	for _, handler := range runtimeExtensionHandlers() {
		name := runtimecatalog.HookName(handler.Hook)
		if forbidden[name] {
			t.Fatalf("handler %q registers in-place hook %s — forbidden: the bootstrap provider owns the in-place extension surface", handler.Name, name)
		}
	}
}

// TestRuntimeHandlersRegister asserts every declared handler registers cleanly
// against the catalog (signature mismatches surface here, not at startup).
func TestRuntimeHandlersRegister(t *testing.T) {
	if len(runtimeExtensionHandlers()) == 0 {
		t.Fatal("no runtime handlers declared")
	}
	srv, err := runtimeserver.New(runtimeserver.Options{Catalog: runtimeCatalog()})
	if err != nil {
		t.Fatalf("building runtime server: %v", err)
	}
	if err := registerRuntimeHooks(srv); err != nil {
		t.Fatalf("registerRuntimeHooks() = %v", err)
	}
}
