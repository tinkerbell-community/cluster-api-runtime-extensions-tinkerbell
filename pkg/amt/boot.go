package amt

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"

	amtboot "github.com/device-management-toolkit/go-wsman-messages/v2/pkg/wsman/amt/boot"
	cimboot "github.com/device-management-toolkit/go-wsman-messages/v2/pkg/wsman/cim/boot"
)

// Instance IDs AMT assigns to the singleton boot objects.
const (
	bootSettingDataInstance = "Intel(r) AMT:BootSettingData 0"
	bootConfigInstance      = "Intel(r) AMT: Boot Configuration 0"
)

// SetBootConfigRole role values.
const (
	// roleIsNextSingleUse arms the override for exactly one boot. AMT clears
	// the boot parameters once consumed, which is precisely the semantics of
	// Redfish BootSourceOverrideEnabled=Once.
	roleIsNextSingleUse = 1
	// roleIsNotNext clears any armed override.
	roleIsNotNext = 32768
)

// BootTarget names a boot device using Redfish vocabulary.
type BootTarget string

const (
	BootPxe       BootTarget = "Pxe"
	BootHdd       BootTarget = "Hdd"
	BootCd        BootTarget = "Cd"
	BootUefiHTTP  BootTarget = "UefiHttp"
	BootBiosSetup BootTarget = "BiosSetup"
)

// ErrUnsupportedBootTarget is returned for a target this device cannot boot.
var ErrUnsupportedBootTarget = errors.New("amt: unsupported boot target")

// ErrVirtualMediaUnsupported is returned when the device firmware does not
// report ForceUEFIHTTPSBoot.
var ErrVirtualMediaUnsupported = errors.New("amt: device does not support UEFI HTTPS boot")

var bootSources = map[BootTarget]cimboot.Source{
	BootPxe:      cimboot.PXE,
	BootHdd:      cimboot.HardDrive,
	BootCd:       cimboot.CD,
	BootUefiHTTP: cimboot.OCRUEFIHTTPS,
}

// SetBootOverride arms a one-shot boot override.
//
// AMT requires three calls in order: write AMT_BootSettingData, select the
// source with ChangeBootOrder, then arm it with SetBootConfigRole. Doing fewer
// leaves the device booting normally while every call still returns success.
//
// Only one-shot overrides are supported. AMT clears the boot parameters once
// consumed, so a persistent override cannot be expressed -- Redfish
// BootSourceOverrideEnabled=Continuous must be rejected by callers.
func (c *Client) SetBootOverride(ctx context.Context, target BootTarget) error {
	if target == BootBiosSetup {
		return c.applyBootSettings(ctx, func(r *amtboot.BootSettingDataRequest) {
			r.BIOSSetup = true
		}, nil)
	}

	source, ok := bootSources[target]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnsupportedBootTarget, target)
	}
	return c.applyBootSettings(ctx, nil, &source)
}

// InsertVirtualMedia arms a one-shot boot from an HTTPS-hosted image.
//
// AMT fetches the image itself, so imageURL must be reachable from the device
// and served with a certificate the device trusts. Nothing here can verify
// that; a device that distrusts the server boots normally and reports no
// error, so the caller should confirm the boot actually happened.
func (c *Client) InsertVirtualMedia(ctx context.Context, imageURL string, enforceSecureBoot bool) error {
	if err := validateImageURL(imageURL); err != nil {
		return err
	}

	caps, err := c.msg.AMT.BootCapabilities.Get()
	if err != nil {
		return fmt.Errorf("amt: reading boot capabilities: %w", err)
	}
	if !caps.Body.BootCapabilitiesGetResponse.ForceUEFIHTTPSBoot {
		return ErrVirtualMediaUnsupported
	}

	param, err := amtboot.NewStringParameter(amtboot.OCR_EFI_NETWORK_DEVICE_PATH, imageURL)
	if err != nil {
		return fmt.Errorf("amt: encoding image URL as a boot parameter: %w", err)
	}
	buf, err := amtboot.CreateTLVBuffer([]amtboot.TLVParameter{param})
	if err != nil {
		return fmt.Errorf("amt: building boot parameter buffer: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(buf)

	source := cimboot.OCRUEFIHTTPS
	return c.applyBootSettings(ctx, func(r *amtboot.BootSettingDataRequest) {
		r.UefiBootParametersArray = encoded
		r.UefiBootNumberOfParams = 1
		r.EnforceSecureBoot = enforceSecureBoot
	}, &source)
}

// EjectVirtualMedia clears any armed boot override, including a pending
// UEFI HTTPS boot.
func (c *Client) EjectVirtualMedia(ctx context.Context) error { return c.ClearBootOverride(ctx) }

// ClearBootOverride disarms any pending one-shot boot override.
func (c *Client) ClearBootOverride(_ context.Context) error {
	resp, err := c.msg.CIM.BootService.SetBootConfigRole(bootConfigInstance, roleIsNotNext)
	if err != nil {
		return fmt.Errorf("amt: clearing boot override: %w", err)
	}
	if rv := resp.Body.SetBootConfigRole_OUTPUT.ReturnValue; rv != 0 {
		return fmt.Errorf("amt: clearing boot override rejected with return value %d", rv)
	}
	return nil
}

// applyBootSettings performs the write/select/arm sequence. mutate customises
// the settings write; source, when non-nil, selects and arms a boot source.
//
// Every field of AMT_BootSettingData is written explicitly rather than merged
// from the current value: the read-only fields AMT returns (BIOSLastStatus,
// RPEEnabled, and friends) are rejected on write, and leaving a stale flag set
// from a previous override is how a device ends up booting the wrong thing.
func (c *Client) applyBootSettings(_ context.Context, mutate func(*amtboot.BootSettingDataRequest), source *cimboot.Source) error {
	cur, err := c.msg.AMT.BootSettingData.Get()
	if err != nil {
		return fmt.Errorf("amt: reading boot setting data: %w", err)
	}

	req := amtboot.BootSettingDataRequest{
		H:            "http://intel.com/wbem/wscim/1/amt-schema/1/AMT_BootSettingData",
		InstanceID:   bootSettingDataInstance,
		ElementName:  cur.Body.BootSettingDataGetResponse.ElementName,
		OwningEntity: cur.Body.BootSettingDataGetResponse.OwningEntity,
	}
	if mutate != nil {
		mutate(&req)
	}

	if _, err := c.msg.AMT.BootSettingData.Put(req); err != nil {
		return fmt.Errorf("amt: writing boot setting data: %w", err)
	}

	if source == nil {
		return nil
	}

	order, err := c.msg.CIM.BootConfigSetting.ChangeBootOrder(*source)
	if err != nil {
		return fmt.Errorf("amt: setting boot order to %q: %w", *source, err)
	}
	if rv := order.Body.ChangeBootOrder_OUTPUT.ReturnValue; rv != 0 {
		return fmt.Errorf("amt: boot order %q rejected with return value %d", *source, rv)
	}

	role, err := c.msg.CIM.BootService.SetBootConfigRole(bootConfigInstance, roleIsNextSingleUse)
	if err != nil {
		return fmt.Errorf("amt: arming boot override: %w", err)
	}
	if rv := role.Body.SetBootConfigRole_OUTPUT.ReturnValue; rv != 0 {
		return fmt.Errorf("amt: arming boot override rejected with return value %d", rv)
	}
	return nil
}

// validateImageURL rejects URLs AMT cannot fetch, before any device state is
// touched. AMT only performs HTTPS boot; an http:// URL is accepted by every
// call in the sequence and then silently fails at boot.
func validateImageURL(raw string) error {
	if raw == "" {
		return errors.New("amt: image URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("amt: parsing image URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("amt: image URL must be https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("amt: image URL must include a host")
	}
	return nil
}
