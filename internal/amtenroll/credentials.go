package amtenroll

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	amtv1 "github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/api/amt/v1alpha1"
	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/pkg/amt"
)

// ErrNoWorkingCredentials is returned when no candidate pair authenticates.
var ErrNoWorkingCredentials = errors.New("amtenroll: no candidate credentials authenticated")

// candidates builds the ordered list of credential pairs to try for a device.
//
// The per-device Secret comes first so a already-enrolled device reconnects
// with the pair known to work. Its previousPassword follows, which is what
// makes a half-failed rotation recoverable: if the new password was written
// but never verified, the device may still be on either value and the next
// reconcile settles it. Profile-wide candidates come last, and are what a
// freshly onboarded device matches.
func (r *Reconciler) candidates(
	ctx context.Context,
	device *amtv1.AMTDevice,
	profile *amtv1.AMTProfile,
) ([]Credentials, error) {
	var out []Credentials
	seen := map[Credentials]bool{}
	add := func(c Credentials) {
		if c.Username == "" || c.Password == "" || seen[c] {
			return
		}
		seen[c] = true
		out = append(out, c)
	}

	if ref := device.Spec.CredentialsRef; ref != nil && ref.Name != "" {
		secret := &corev1.Secret{}
		key := client.ObjectKey{Namespace: device.Namespace, Name: ref.Name}
		switch err := r.Client.Get(ctx, key, secret); {
		case err == nil:
			user := string(secret.Data[SecretKeyUsername])
			add(Credentials{Username: user, Password: string(secret.Data[SecretKeyPassword])})
			add(Credentials{Username: user, Password: string(secret.Data[SecretKeyPreviousPassword])})
		case apierrors.IsNotFound(err):
			// The Secret was deleted out from under us. Fall through to the
			// profile candidates rather than failing: that is exactly the
			// path that recovers the device.
		default:
			return nil, fmt.Errorf("reading credentials secret %s: %w", ref.Name, err)
		}
	}

	for _, ref := range profile.Spec.CredentialSources {
		namespace := ref.Namespace
		if namespace == "" {
			namespace = device.Namespace
		}
		secret := &corev1.Secret{}
		key := client.ObjectKey{Namespace: namespace, Name: ref.Name}
		if err := r.Client.Get(ctx, key, secret); err != nil {
			if apierrors.IsNotFound(err) {
				r.Log.Warn("credential source secret not found; skipping",
					"secret", ref.Name, "namespace", namespace)
				continue
			}
			return nil, fmt.Errorf("reading credential source %s/%s: %w", namespace, ref.Name, err)
		}
		add(Credentials{
			Username: string(secret.Data[SecretKeyUsername]),
			Password: string(secret.Data[SecretKeyPassword]),
		})
	}

	if len(out) == 0 {
		return nil, ErrNoWorkingCredentials
	}
	return out, nil
}

// verified is a device connection that has authenticated.
type verified struct {
	client      *amt.Client
	credentials Credentials
	fingerprint string
	facts       *amt.Facts
}

// connect pivots through candidates until one authenticates, and returns the
// working connection along with the facts read to prove it.
//
// Authentication is proven by actually reading facts rather than by opening a
// connection: WS-Man is stateless over HTTP digest, so a "connection" that has
// not carried a successful authenticated request proves nothing.
func (r *Reconciler) connect(
	ctx context.Context,
	device *amtv1.AMTDevice,
	candidates []Credentials,
) (*verified, error) {
	host := device.Spec.Endpoint.Host
	port := int(device.Spec.Endpoint.Port)

	// The pin is learned on first contact and enforced from then on. Learning
	// it is trust-on-first-use, which is the strongest guarantee available
	// against a device whose certificate is self-signed by the factory.
	pin := device.Status.TLSFingerprint
	if pin == "" && port != amt.PortPlaintext {
		probe, err := amt.New(amt.Config{Host: host, Port: port, Timeout: r.Timeout})
		if err != nil {
			return nil, err
		}
		fp, err := probe.Fingerprint(ctx)
		if err != nil {
			return nil, fmt.Errorf("learning device certificate: %w", err)
		}
		pin = fp
		r.Log.Info("learned device certificate fingerprint",
			"device", device.Name, "fingerprint", fp)
	}

	var lastErr error
	for _, cred := range candidates {
		c, err := amt.New(amt.Config{
			Host:              host,
			Port:              port,
			Username:          cred.Username,
			Password:          cred.Password,
			Timeout:           r.Timeout,
			PinnedFingerprint: pin,
		})
		if err != nil {
			return nil, err
		}

		facts, err := c.Facts(ctx)
		if err != nil {
			lastErr = err
			r.Log.Debug("candidate credentials rejected",
				"device", device.Name, "username", cred.Username, "err", err)
			continue
		}
		return &verified{client: c, credentials: cred, fingerprint: pin, facts: facts}, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("%w: last error: %w", ErrNoWorkingCredentials, lastErr)
	}
	return nil, ErrNoWorkingCredentials
}
