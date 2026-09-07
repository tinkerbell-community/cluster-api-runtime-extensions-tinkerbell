package amt_test

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/pkg/amt"
)

// Integration tests run only when AMT_HOST is set, so the default `go test`
// run stays hermetic:
//
//	AMT_HOST=10.0.0.160 AMT_USER=admin AMT_PASS='…' go test ./pkg/amt/ -run Integration -v
//
// These are read-only. Nothing here changes power state or boot configuration.
func testClient(t *testing.T) *amt.Client {
	t.Helper()

	host := os.Getenv("AMT_HOST")
	if host == "" {
		t.Skip("AMT_HOST not set; skipping live-device integration test")
	}

	port := amt.PortTLS
	if p := os.Getenv("AMT_PORT"); p != "" {
		v, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("AMT_PORT: %v", err)
		}
		port = v
	}

	c, err := amt.New(amt.Config{
		Host:              host,
		Port:              port,
		Username:          os.Getenv("AMT_USER"),
		Password:          os.Getenv("AMT_PASS"),
		PinnedFingerprint: os.Getenv("AMT_FINGERPRINT"),
	})
	if err != nil {
		t.Fatalf("amt.New: %v", err)
	}
	return c
}

func TestIntegrationFingerprint(t *testing.T) {
	c := testClient(t)

	fp, err := c.Fingerprint(context.Background())
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if len(fp) != 64 {
		t.Fatalf("Fingerprint returned %d characters, want 64 hex", len(fp))
	}
	t.Logf("device certificate fingerprint: %s", fp)
}

func TestIntegrationFacts(t *testing.T) {
	c := testClient(t)

	f, err := c.Facts(context.Background())
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}

	t.Logf("platformGUID=%s", f.PlatformGUID)
	t.Logf("provisioningState=%s controlMode=%s allowed=%v",
		f.ProvisioningState, f.ControlMode, f.AllowedControlModes)
	t.Logf("firmware amt=%s build=%s sku=%s", f.Firmware.AMT, f.Firmware.Build, f.Firmware.SKU)
	t.Logf("bootCapabilities=%+v", f.BootCapabilities)
	t.Logf("redirection=%+v", f.Redirection)
	t.Logf("digestRealm=%s dnsSuffix=%s", f.DigestRealm, f.DNSSuffix)

	if f.ProvisioningState == "" {
		t.Error("provisioning state is empty")
	}
	// A reachable, authenticated device must report a platform GUID; it is the
	// identity every downstream resource is keyed on.
	if f.Provisioned() && f.PlatformGUID == "" {
		t.Error("provisioned device reported no platform GUID")
	}
	if f.Provisioned() && f.DigestRealm == "" {
		t.Error("provisioned device reported no digest realm; password rotation would be impossible")
	}
}

func TestIntegrationInventory(t *testing.T) {
	c := testClient(t)

	inv, err := c.Inventory(context.Background())
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}

	s := inv.Summary
	t.Logf("manufacturer=%q model=%q serial=%q", s.Manufacturer, s.Model, s.SerialNumber)
	t.Logf("baseboard=%q/%q bios=%q", s.BaseboardModel, s.BaseboardSerial, s.BIOSVersion)
	t.Logf("cpus=%d model=%q memory=%d bytes", s.CPUCount, s.CPUModel, s.MemoryBytes)
	t.Logf("nics=%v", s.NICs)

	if s.Model == "" {
		t.Error("inventory reported no model")
	}
	if s.SerialNumber == "" {
		t.Error("inventory reported no serial number")
	}
	if inv.Device == nil {
		t.Fatal("inventory returned no common.Device")
	}
	if inv.Device.Model != s.Model {
		t.Errorf("common.Device.Model = %q but summary says %q", inv.Device.Model, s.Model)
	}
	// Every NIC surfaced in the summary must also be reachable through the
	// common.Device shape the Tinkerbell resources are built from.
	if len(s.NICs) != len(inv.Device.NICs) {
		t.Errorf("summary has %d NICs, common.Device has %d", len(s.NICs), len(inv.Device.NICs))
	}
}

func TestIntegrationPowerState(t *testing.T) {
	c := testClient(t)

	state, err := c.PowerState(context.Background())
	if err != nil {
		t.Fatalf("PowerState: %v", err)
	}
	t.Logf("power state: %s", state)

	switch state {
	case amt.PowerOn, amt.PowerOff, amt.PowerUnknown:
	default:
		t.Errorf("unexpected power state %q", state)
	}
}

// A wrong pin must fail closed. Without this the pinning feature could be
// silently inert and nobody would notice until it mattered.
func TestIntegrationPinnedFingerprintMismatchIsRefused(t *testing.T) {
	host := os.Getenv("AMT_HOST")
	if host == "" {
		t.Skip("AMT_HOST not set; skipping live-device integration test")
	}

	c, err := amt.New(amt.Config{
		Host:     host,
		Username: os.Getenv("AMT_USER"),
		Password: os.Getenv("AMT_PASS"),
		// Valid shape, wrong value.
		PinnedFingerprint: "0000000000000000000000000000000000000000000000000000000000000000",
	})
	if err != nil {
		t.Fatalf("amt.New: %v", err)
	}

	if _, err := c.Facts(context.Background()); err == nil {
		t.Fatal("Facts succeeded against a mismatched certificate pin; pinning is not enforced")
	} else {
		t.Logf("pin correctly refused: %v", err)
	}
}
