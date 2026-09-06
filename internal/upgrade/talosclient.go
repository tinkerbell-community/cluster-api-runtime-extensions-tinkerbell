package upgrade

import (
	"context"
	"fmt"

	"github.com/blang/semver/v4"
	"github.com/cosi-project/runtime/pkg/state"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
)

// TalosConn is the slice of the Talos API the coordinator consumes: one version
// read and the COSI state the manifests live in. An interface so the reconciler
// tests can fake the whole Talos side.
type TalosConn interface {
	// ObservedVersion returns the Talos version reported by the given node.
	ObservedVersion(ctx context.Context, node string) (semver.Version, error)
	// State is the COSI state, routed to a node via client.WithNode contexts.
	State() state.State
	Close() error
}

// NewTalosConn dials the Talos API with the cluster's talosconfig, overriding
// endpoints with the given control-plane addresses (CACPPT's own access pattern).
func NewTalosConn(ctx context.Context, talosconfig []byte, endpoints []string) (TalosConn, error) {
	cfg, err := clientconfig.FromBytes(talosconfig)
	if err != nil {
		return nil, fmt.Errorf("parsing talosconfig: %w", err)
	}
	opts := []talosclient.OptionFunc{
		talosclient.WithConfig(cfg),
		talosclient.WithDefaultGRPCDialOptions(),
	}
	if len(endpoints) > 0 {
		opts = append(opts, talosclient.WithEndpoints(endpoints...))
	}
	c, err := talosclient.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("building talos client: %w", err)
	}
	return &talosConn{c: c}, nil
}

type talosConn struct {
	c *talosclient.Client
}

func (t *talosConn) ObservedVersion(ctx context.Context, node string) (semver.Version, error) {
	if node != "" {
		ctx = talosclient.WithNode(ctx, node)
	}
	resp, err := t.c.Version(ctx)
	if err != nil {
		return semver.Version{}, fmt.Errorf("reading talos version: %w", err)
	}
	messages := resp.GetMessages()
	if len(messages) == 0 || messages[0].GetVersion() == nil {
		return semver.Version{}, fmt.Errorf("talos version response carried no version")
	}
	v, err := semver.ParseTolerant(messages[0].GetVersion().GetTag())
	if err != nil {
		return semver.Version{}, fmt.Errorf("parsing talos version %q: %w", messages[0].GetVersion().GetTag(), err)
	}
	return v, nil
}

func (t *talosConn) State() state.State { return t.c.COSI }

func (t *talosConn) Close() error { return t.c.Close() }

// WithTalosNode routes COSI reads to a specific node.
func WithTalosNode(ctx context.Context, node string) context.Context {
	return talosclient.WithNode(ctx, node)
}
