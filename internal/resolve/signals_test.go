package resolve

import (
	"strings"
	"testing"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func hardware(annotations map[string]string, arch string, disks ...string) *tinkv1.Hardware {
	hw := &tinkv1.Hardware{ObjectMeta: metav1.ObjectMeta{Name: "hw-1", Namespace: "tinkerbell", Annotations: annotations}}
	for _, d := range disks {
		hw.Spec.Disks = append(hw.Spec.Disks, tinkv1.Disk{Device: d})
	}
	if arch != "" {
		hw.Spec.Interfaces = []tinkv1.Interface{{DHCP: &tinkv1.DHCP{Arch: arch}}}
	}
	return hw
}

const testTootles = "http://10.0.0.1:7080/2009-04-04/user-data"

func TestArchitectureMapping(t *testing.T) {
	t.Parallel()
	for arch, want := range map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64", "": "amd64", "weird": "amd64"} {
		if got := architectureOf(hardware(nil, arch)); got != want {
			t.Errorf("arch %q = %q, want %q", arch, got, want)
		}
	}
}

// §3.4: extensions are the union of Hardware + TinkerbellMachine annotations, plus NVMe.
func TestSignalsUnionExtensionsAndNVMe(t *testing.T) {
	t.Parallel()
	hw := hardware(map[string]string{ExtensionsAnnotation: "siderolabs/gvisor"}, "x86_64", "/dev/nvme0n1")
	sig := SignalsFromHardware(hw, []string{"siderolabs/intel-ucode"}, CustomizationConfig{TootlesUserDataURL: testTootles})
	exts := Build(sig).Customization.SystemExtensions.OfficialExtensions
	if len(exts) != 3 {
		t.Fatalf("extensions = %v, want gvisor + intel-ucode + nvme-cli", exts)
	}
}

// §3.2: kernel args carry the constants + talos.config; arm64 implies sd-boot; the overlay
// annotation flows through. Without talos.config the node boots into maintenance mode.
func TestSignalsCarryP2BootableInputs(t *testing.T) {
	t.Parallel()
	hw := hardware(map[string]string{
		OverlayAnnotation:         "rpi_generic@siderolabs/sbc-raspberrypi",
		ExtraKernelArgsAnnotation: "sysctl.vm.nr_hugepages=1024",
	}, "aarch64")
	sig := SignalsFromHardware(hw, nil, CustomizationConfig{TootlesUserDataURL: testTootles})

	joined := strings.Join(sig.ExtraKernelArgs, " ")
	for _, want := range []string{"console=tty0", "console=ttyAMA0,115200", "net.ifnames=0", "talos.config=" + testTootles, "sysctl.vm.nr_hugepages=1024"} {
		if !strings.Contains(joined, want) {
			t.Errorf("kernel args %q missing %q", joined, want)
		}
	}
	if sig.Bootloader != "sd-boot" {
		t.Errorf("arm64 bootloader = %q, want sd-boot", sig.Bootloader)
	}
	if sig.Overlay == nil || sig.Overlay.Name != "rpi_generic" || sig.Overlay.Image != "siderolabs/sbc-raspberrypi" {
		t.Errorf("overlay = %+v, want {rpi_generic siderolabs/sbc-raspberrypi}", sig.Overlay)
	}
}

func TestAmd64HasNoBootloaderOverride(t *testing.T) {
	t.Parallel()
	sig := SignalsFromHardware(hardware(nil, "x86_64"), nil, CustomizationConfig{TootlesUserDataURL: testTootles})
	if sig.Bootloader != "" {
		t.Errorf("amd64 bootloader = %q, want empty", sig.Bootloader)
	}
}
