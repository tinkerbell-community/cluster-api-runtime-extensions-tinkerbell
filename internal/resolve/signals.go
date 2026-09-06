package resolve

import (
	"strings"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
)

// SignalsFromHardware extracts the schematic-relevant facts from a Hardware object and folds
// in the P2 bootable-image inputs (§3.2/§3.4): the extension union, the assembled kernel args,
// the C1 overlay annotation, and the arm64 bootloader. extraFromMachine lets a TinkerbellMachine
// contribute extensions without editing the shared Hardware record.
func SignalsFromHardware(hw *tinkv1.Hardware, extraFromMachine []string, cfg CustomizationConfig) Signals {
	annotations := hw.GetAnnotations()
	arch := architectureOf(hw)

	signals := Signals{
		Architecture:    arch,
		ExtraExtensions: append(parseExtensions(annotations[ExtensionsAnnotation]), extraFromMachine...),
		ExtraKernelArgs: assembleKernelArgs(cfg.TootlesUserDataURL, annotations[ExtraKernelArgsAnnotation]),
		Overlay:         parseOverlay(annotations[OverlayAnnotation]),
		Bootloader:      bootloaderFor(arch),
	}
	for _, disk := range hw.Spec.Disks {
		if disk.Device != "" {
			signals.DiskDevices = append(signals.DiskDevices, disk.Device)
		}
	}
	return signals
}

// architectureOf maps the iPXE architecture Tinkerbell records onto the Talos architecture
// name, defaulting to amd64 when nothing usable is present.
func architectureOf(hw *tinkv1.Hardware) string {
	for _, iface := range hw.Spec.Interfaces {
		if iface.DHCP == nil || iface.DHCP.Arch == "" {
			continue
		}
		switch strings.ToLower(iface.DHCP.Arch) {
		case "aarch64", archARM64:
			return archARM64
		case "x86_64", archAMD64, "x86":
			return archAMD64
		}
	}
	return archAMD64
}

// parseExtensions splits the comma-separated system-extensions annotation.
func parseExtensions(value string) []string {
	return splitCSV(value)
}
