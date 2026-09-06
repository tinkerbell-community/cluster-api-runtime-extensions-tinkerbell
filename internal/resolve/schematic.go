package resolve

import (
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Signals are the facts about a machine that determine its schematic.
type Signals struct {
	// Architecture is the Talos architecture name, "amd64" or "arm64".
	Architecture string
	// DiskDevices are the block device paths declared on the Hardware.
	DiskDevices []string
	// ExtraExtensions are operator-supplied official extensions (Hardware + TinkerbellMachine).
	ExtraExtensions []string
	// ExtraKernelArgs are the bootable-image kernel args (constants + talos.config + C1 args).
	ExtraKernelArgs []string
	// Overlay is the Image Factory overlay, when one applies (rpi et al).
	Overlay *Overlay
	// Bootloader is the Image Factory bootloader ("sd-boot" for arm64).
	Bootloader string
}

// Overlay is the Image Factory customization.overlay block.
type Overlay struct {
	Name  string `json:"name,omitempty"`
	Image string `json:"image,omitempty"`
}

// Schematic is the Image Factory schematic document.
type Schematic struct {
	Customization Customization `json:"customization"`
}

// Customization holds the schematic's customization blocks.
//
// Unlike CAPT's installer-only engine this models the bootable-image inputs too: the
// Factory ignores extraKernelArgs/overlay/bootloader for the metal-installer image but
// HONORS them for the raw disk image, so one content-addressed ID backs both (§3.2).
type Customization struct {
	SystemExtensions SystemExtensions `json:"systemExtensions,omitempty"`
	ExtraKernelArgs  []string         `json:"extraKernelArgs,omitempty"`
	Overlay          *Overlay         `json:"overlay,omitempty"`
	Bootloader       string           `json:"bootloader,omitempty"`
}

// SystemExtensions lists the official extensions baked into the image.
type SystemExtensions struct {
	OfficialExtensions []string `json:"officialExtensions,omitempty"`
}

// Build applies the built-in rules to signals and returns the resulting schematic.
//
// Extensions are deduplicated and sorted so identical hardware yields an identical
// content hash and the Factory returns a stable ID. Kernel args, overlay and bootloader
// are passed through as already assembled by SignalsFromHardware (their order is stable).
func Build(signals Signals) Schematic {
	extensions := map[string]struct{}{}
	for _, extension := range signals.ExtraExtensions {
		extensions[extension] = struct{}{}
	}
	if hasNVMeDisk(signals.DiskDevices) {
		extensions[nvmeCLIExtension] = struct{}{}
	}
	names := make([]string, 0, len(extensions))
	for name := range extensions {
		names = append(names, name)
	}
	sort.Strings(names)

	return Schematic{Customization: Customization{
		SystemExtensions: SystemExtensions{OfficialExtensions: names},
		ExtraKernelArgs:  signals.ExtraKernelArgs,
		Overlay:          signals.Overlay,
		Bootloader:       signals.Bootloader,
	}}
}

// hasNVMeDisk reports whether any declared disk is an NVMe namespace.
func hasNVMeDisk(devices []string) bool {
	for _, device := range devices {
		if strings.HasPrefix(device, "/dev/nvme") {
			return true
		}
	}
	return false
}

// Marshal renders the schematic as the YAML the Image Factory expects.
func (s Schematic) Marshal() ([]byte, error) {
	encoded, err := yaml.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshalling schematic: %w", err)
	}
	return encoded, nil
}

// InstallerImage returns the installer reference for machine.install.image and the
// resolver-owned installer-image annotation: factory.talos.dev/metal-installer/<id>:<version>.
func InstallerImage(factoryURL, id, talosVersion string) string {
	return fmt.Sprintf("%s/metal-installer/%s:%s", registryHost(factoryURL), id, talosVersion)
}

// registryHost strips the scheme so a factory URL can be used as an image registry host.
func registryHost(factoryURL string) string {
	host := strings.TrimSuffix(factoryURL, "/")
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	return host
}
