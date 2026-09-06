package upgrade

import (
	"context"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestWriteCheckpoint(t *testing.T) {
	u := tcpFixture(map[string]any{"version": "v1.36.4"}, nil, nil, nil)
	c := fake.NewClientBuilder().WithScheme(upgradeScheme(t)).WithObjects(u).Build()

	if err := WriteCheckpoint(context.Background(), c, u, "k8s=v1.36.4,talos=v1.13.9"); err != nil {
		t.Fatalf("WriteCheckpoint() = %v", err)
	}
	got := tcpFixture(nil, nil, nil, nil)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "tink", Name: "cp"}, got); err != nil {
		t.Fatal(err)
	}
	if LastSynced(got) != "k8s=v1.36.4,talos=v1.13.9" {
		t.Errorf("annotation after checkpoint = %q", LastSynced(got))
	}
}

func TestClearForceSync(t *testing.T) {
	u := tcpFixture(map[string]any{"version": "v1.36.4"},
		map[string]any{ForceSyncAnnotation: "true", "keep": "me"}, nil, nil)
	c := fake.NewClientBuilder().WithScheme(upgradeScheme(t)).WithObjects(u).Build()

	if err := ClearForceSync(context.Background(), c, u); err != nil {
		t.Fatalf("ClearForceSync() = %v", err)
	}
	got := tcpFixture(nil, nil, nil, nil)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "tink", Name: "cp"}, got); err != nil {
		t.Fatal(err)
	}
	if ForceSyncRequested(got) {
		t.Error("force-sync annotation survived ClearForceSync")
	}
	if got.GetAnnotations()["keep"] != "me" {
		t.Error("ClearForceSync removed an unrelated annotation")
	}
}
