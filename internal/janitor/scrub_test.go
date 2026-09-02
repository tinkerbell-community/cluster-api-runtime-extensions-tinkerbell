package janitor

import (
	"testing"
	"time"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	"k8s.io/utils/ptr"
)

func testOptions() Options {
	return Options{
		ClearOSMetadata:  true,
		BaselineAllowPXE: false,
		Now:              func() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) },
	}
}

func releasedHardware() *tinkv1.Hardware {
	hw := hardwareWith(false, "#cloud-config secret-bearing", false)
	hw.Spec.Metadata = &tinkv1.HardwareMetadata{
		Manufacturer: &tinkv1.MetadataManufacturer{Slug: "asrockrack"},
		Instance: &tinkv1.MetadataInstance{
			ID:              "aa:bb:cc:dd:ee:01",
			Hostname:        "talos-aabbccddee01",
			State:           "provisioned",
			OperatingSystem: &tinkv1.MetadataInstanceOperatingSystem{Slug: "abc123", Version: "v1.13.9"},
		},
	}
	hw.Spec.Interfaces = []tinkv1.Interface{
		{
			Netboot: &tinkv1.Netboot{AllowPXE: ptr.To(true), AllowWorkflow: ptr.To(true)},
			DHCP:    &tinkv1.DHCP{MAC: "aa:bb:cc:dd:ee:01", Hostname: "talos-aabbccddee01"},
		},
		{DHCP: &tinkv1.DHCP{MAC: "aa:bb:cc:dd:ee:02"}}, // no netboot block
	}
	return hw
}

func TestScrub(t *testing.T) {
	hw := releasedHardware()
	if len(Scrub(hw, testOptions())) == 0 {
		t.Fatal("Scrub reported no change on a released object")
	}

	if hw.Spec.UserData != nil {
		t.Errorf("userData not cleared: %v", *hw.Spec.UserData)
	}
	instance := hw.Spec.Metadata.Instance
	if instance.OperatingSystem != nil {
		t.Errorf("operating_system not cleared: %+v", instance.OperatingSystem)
	}
	if instance.State != "" {
		t.Errorf("instance state not cleared: %q", instance.State)
	}
	// Untargeted fields survive: the scrub mutates in place, never rebuilds.
	if instance.ID != "aa:bb:cc:dd:ee:01" || instance.Hostname != "talos-aabbccddee01" {
		t.Errorf("scrub touched identity fields: %+v", instance)
	}
	if hw.Spec.Metadata.Manufacturer == nil || hw.Spec.Metadata.Manufacturer.Slug != "asrockrack" {
		t.Errorf("scrub touched manufacturer: %+v", hw.Spec.Metadata.Manufacturer)
	}

	nb := hw.Spec.Interfaces[0].Netboot
	if nb.AllowPXE == nil || *nb.AllowPXE {
		t.Errorf("allowPXE not parked: %+v", nb)
	}
	if nb.AllowWorkflow == nil || !*nb.AllowWorkflow {
		t.Errorf("scrub touched allowWorkflow: %+v", nb)
	}
	if hw.Spec.Interfaces[1].Netboot != nil {
		t.Error("scrub invented a netboot block on an interface without one")
	}
	if hw.Annotations[ScrubbedAtAnnotation] != "2026-09-01T12:00:00Z" {
		t.Errorf("scrubbed-at = %q", hw.Annotations[ScrubbedAtAnnotation])
	}

	// Idempotent re-entry: a second scrub changes nothing.
	if changed := Scrub(hw, testOptions()); len(changed) != 0 {
		t.Errorf("second Scrub reported changes %v; must be self-extinguishing", changed)
	}
}

func TestScrubKeepsOSMetadataWhenDisabled(t *testing.T) {
	hw := releasedHardware()
	opts := testOptions()
	opts.ClearOSMetadata = false

	if len(Scrub(hw, opts)) == 0 {
		t.Fatal("Scrub reported no change")
	}
	if hw.Spec.UserData != nil {
		t.Error("userData must be cleared regardless of the OS-metadata flag")
	}
	if hw.Spec.Metadata.Instance.OperatingSystem == nil {
		t.Error("operating_system cleared despite --clear-os-metadata=false")
	}
	if hw.Spec.Metadata.Instance.State != "provisioned" {
		t.Error("instance state cleared despite --clear-os-metadata=false")
	}
}

func TestScrubBaselineAllowPXETrue(t *testing.T) {
	hw := releasedHardware()
	hw.Spec.Interfaces[0].Netboot.AllowPXE = ptr.To(false)
	opts := testOptions()
	opts.BaselineAllowPXE = true

	if len(Scrub(hw, opts)) == 0 {
		t.Fatal("Scrub reported no change")
	}
	if nb := hw.Spec.Interfaces[0].Netboot; nb.AllowPXE == nil || !*nb.AllowPXE {
		t.Errorf("baseline true not asserted: %+v", nb)
	}
}

func TestScrubNoChangeOnCleanObject(t *testing.T) {
	// An object with nothing to scrub (post-scrub shape) yields no change
	// and no scrubbed-at refresh.
	hw := hardwareWith(false, "", false)
	hw.Spec.Interfaces = []tinkv1.Interface{{
		Netboot: &tinkv1.Netboot{AllowPXE: ptr.To(false)},
	}}
	if changed := Scrub(hw, testOptions()); len(changed) != 0 {
		t.Errorf("Scrub reported changes on a clean object: %v", changed)
	}
	if _, ok := hw.Annotations[ScrubbedAtAnnotation]; ok {
		t.Error("scrubbed-at stamped without changes")
	}
}
