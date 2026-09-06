package upgrade

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WorkloadKubeconfig reads the CAPI-convention kubeconfig secret
// (<cluster>-kubeconfig, key "value") for the workload cluster.
func WorkloadKubeconfig(ctx context.Context, c client.Client, namespace, cluster string) ([]byte, error) {
	return secretKey(ctx, c, namespace, cluster+"-kubeconfig", "value")
}

// TalosClientConfig reads the Talos client config secret (<cluster>-talosconfig,
// key "talosconfig"), whose endpoints CABPT keeps fresh.
func TalosClientConfig(ctx context.Context, c client.Client, namespace, cluster string) ([]byte, error) {
	return secretKey(ctx, c, namespace, cluster+"-talosconfig", "talosconfig")
}

func secretKey(ctx context.Context, c client.Client, namespace, name, key string) ([]byte, error) {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, secret); err != nil {
		return nil, fmt.Errorf("reading secret %s/%s: %w", namespace, name, err)
	}
	data, ok := secret.Data[key]
	if !ok || len(data) == 0 {
		return nil, fmt.Errorf("secret %s/%s has no %q key", namespace, name, key)
	}
	return data, nil
}

// WorkloadRESTConfig parses a kubeconfig into a rest.Config.
func WorkloadRESTConfig(kubeconfig []byte) (*rest.Config, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("parsing workload kubeconfig: %w", err)
	}
	return cfg, nil
}
