// Package amt talks to Intel AMT devices over WS-Management.
//
// It is the only package that knows WS-Man exists. Everything above it works
// with the plain structs defined here and with bmc-toolbox/common.Device.
//
// It exists because bmclib's intelamt provider implements only PowerSet,
// PowerStateGet and BootDeviceSet(pxe) -- no inventory, no virtual media, no
// certificate handling -- and its underlying transport does not verify the
// device's TLS certificate at all.
package amt

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/device-management-toolkit/go-wsman-messages/v2/pkg/wsman"
	"github.com/device-management-toolkit/go-wsman-messages/v2/pkg/wsman/client"
)

// Standard AMT WS-Man ports. The underlying library derives the port from
// whether TLS is in use, so these are the only two we can address.
const (
	PortTLS       = 16993
	PortPlaintext = 16992
)

// DefaultTimeout is the per-operation timeout. The AMT firmware is slow to
// wake, so this is generous.
const DefaultTimeout = 30 * time.Second

// ErrFingerprintMismatch is returned when a pinned certificate does not match
// the certificate the device presents.
var ErrFingerprintMismatch = errors.New("amt: device certificate does not match pinned fingerprint")

// ErrUnsupportedPort is returned for a port other than 16992 or 16993.
var ErrUnsupportedPort = errors.New("amt: only ports 16992 (plaintext) and 16993 (TLS) are supported")

// Config describes how to reach one AMT device.
type Config struct {
	// Host is the device address.
	Host string

	// Port must be PortTLS or PortPlaintext. Zero means PortTLS.
	Port int

	// Username and Password authenticate over HTTP digest.
	Username string
	Password string

	// Timeout bounds each operation. Zero means DefaultTimeout.
	Timeout time.Duration

	// PinnedFingerprint, when set, is the hex-encoded SHA-256 of the device's
	// DER leaf certificate. A device presenting anything else is refused.
	//
	// AMT ships a self-signed certificate, so chain verification can never
	// succeed on a factory device. Pinning is what makes the connection
	// trustworthy instead.
	PinnedFingerprint string
}

func (c Config) timeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultTimeout
	}
	return c.Timeout
}

func (c Config) port() int {
	if c.Port == 0 {
		return PortTLS
	}
	return c.Port
}

func (c Config) useTLS() bool { return c.port() == PortTLS }

func (c Config) validate() error {
	if c.Host == "" {
		return errors.New("amt: host is required")
	}
	if p := c.port(); p != PortTLS && p != PortPlaintext {
		return fmt.Errorf("%w: got %d", ErrUnsupportedPort, p)
	}
	return nil
}

// Client is a connection to one AMT device.
//
// Every method takes a context, but only those that dial directly (Fingerprint)
// can honour cancellation: the underlying WS-Man library predates context and
// bounds its own calls with Config.Timeout instead. The context is accepted
// throughout so callers in a reconciler do not have to special-case this, and
// so cancellation becomes real if the library gains support.
type Client struct {
	cfg Config
	msg wsman.Messages
}

// New builds a client. It does not contact the device; the first call that
// needs the network does.
func New(cfg Config) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	params := client.Parameters{
		Target:    cfg.Host,
		Username:  cfg.Username,
		Password:  cfg.Password,
		UseDigest: true,
		UseTLS:    cfg.useTLS(),
		// AMT's factory certificate is self-signed, so chain verification can
		// never pass. Trust is established by the pin below, not the chain.
		SelfSignedAllowed: true,
		PinnedCert:        cfg.PinnedFingerprint,
		Timeout:           cfg.timeout(),
	}

	return &Client{cfg: cfg, msg: wsman.NewMessages(params)}, nil
}

// Address is the host:port this client talks to.
func (c *Client) Address() string {
	return net.JoinHostPort(c.cfg.Host, strconv.Itoa(c.cfg.port()))
}

// Fingerprint dials the device and returns the hex SHA-256 of its leaf
// certificate, without authenticating.
//
// It is used to learn a fingerprint on first contact so it can be pinned for
// every subsequent connection. It returns an error on a plaintext endpoint,
// which presents no certificate.
func (c *Client) Fingerprint(ctx context.Context) (string, error) {
	if !c.cfg.useTLS() {
		return "", errors.New("amt: plaintext endpoint presents no certificate")
	}

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: c.cfg.timeout()},
		Config: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // the pin, not the chain, is the trust anchor
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp", c.Address())
	if err != nil {
		return "", fmt.Errorf("amt: dialing %s: %w", c.Address(), err)
	}
	defer conn.Close()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return "", errors.New("amt: dialer did not return a TLS connection")
	}
	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("amt: device presented no certificate")
	}
	return Fingerprint(certs[0].Raw), nil
}

// Fingerprint returns the hex-encoded SHA-256 of a DER certificate, in the
// form the pinning verifier expects.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}
