package upgrade

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WriteCheckpoint records the successfully synced pair on the TalosControlPlane.
// Metadata-only JSON merge patch under the coordinator's own field manager:
// annotation keys merge independently, so this can never conflict with CACPPT's
// patch-helper writes, and merge semantics are required anyway to *remove*
// force-sync (SSA cannot unset a key another manager owns). This is the
// component's only management-cluster write.
func WriteCheckpoint(ctx context.Context, c client.Client, tcp *unstructured.Unstructured, pair string) error {
	return patchAnnotations(ctx, c, tcp, map[string]*string{LastSyncedAnnotation: &pair})
}

// ClearForceSync removes the one-shot gate-bypass annotation after honoring it.
func ClearForceSync(ctx context.Context, c client.Client, tcp *unstructured.Unstructured) error {
	return patchAnnotations(ctx, c, tcp, map[string]*string{ForceSyncAnnotation: nil})
}

func patchAnnotations(ctx context.Context, c client.Client, tcp *unstructured.Unstructured, annotations map[string]*string) error {
	body, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": annotations}})
	if err != nil {
		return fmt.Errorf("marshaling annotation patch: %w", err)
	}
	target := &unstructured.Unstructured{}
	target.SetGroupVersionKind(tcp.GroupVersionKind())
	target.SetNamespace(tcp.GetNamespace())
	target.SetName(tcp.GetName())
	if err := c.Patch(ctx, target, client.RawPatch(types.MergePatchType, body), client.FieldOwner(FieldManager)); err != nil {
		return fmt.Errorf("patching %s annotations on %s/%s: %w", tcp.GetKind(), tcp.GetNamespace(), tcp.GetName(), err)
	}
	return nil
}
