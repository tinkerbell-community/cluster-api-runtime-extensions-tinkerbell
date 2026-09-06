package resolve

import (
	"strings"
	"testing"
)

func TestBuildDeduplicatesAndSortsExtensionsWithNVMeRule(t *testing.T) {
	t.Parallel()
	got := Build(Signals{
		Architecture:    archAMD64,
		DiskDevices:     []string{"/dev/sda", "/dev/nvme0n1"},
		ExtraExtensions: []string{"siderolabs/gvisor", "siderolabs/nvme-cli"},
	}).Customization.SystemExtensions.OfficialExtensions
	want := []string{"siderolabs/gvisor", "siderolabs/nvme-cli"}
	if len(got) != len(want) {
		t.Fatalf("extensions = %v, want %v (deduped, nvme-cli once)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extensions = %v, want sorted %v", got, want)
		}
	}
}

// P2 (§3.2): a single content-addressed schematic must carry the bootable-image
// inputs the Factory ignores for the installer but honors for the raw disk. Without
// these the node boots into maintenance mode.
func TestBuildCarriesP2CustomizationIntoMarshaledDocument(t *testing.T) {
	t.Parallel()
	doc, err := Build(Signals{
		Architecture:    archARM64,
		ExtraKernelArgs: []string{"talos.config=http://10.0.0.1:7080/2009-04-04/user-data", "console=tty0", "net.ifnames=0"},
		Overlay:         &Overlay{Name: "rpi_generic", Image: "siderolabs/sbc-raspberrypi"},
		Bootloader:      "sd-boot",
	}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"extraKernelArgs", "talos.config=http://10.0.0.1:7080/2009-04-04/user-data",
		"console=tty0", "net.ifnames=0", "overlay", "rpi_generic", "siderolabs/sbc-raspberrypi", "bootloader", "sd-boot",
	} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("marshaled schematic missing %q:\n%s", want, doc)
		}
	}
}

func TestInstallerImageReference(t *testing.T) {
	t.Parallel()
	const id = "9ed5fecdacb36b5c5427b87d409f1065cfb2df69b0f71c58b868d9d466d8dab3"
	got := InstallerImage(DefaultFactoryURL, id, "v1.14.0-rc.1")
	want := "factory.talos.dev/metal-installer/" + id + ":v1.14.0-rc.1"
	if got != want {
		t.Errorf("installer = %q, want %q", got, want)
	}
}

// Identical hardware must marshal identically or the resolved ID churns every reconcile.
func TestSchematicIsDeterministic(t *testing.T) {
	t.Parallel()
	a, err := Build(Signals{ExtraExtensions: []string{"siderolabs/gvisor", "siderolabs/intel-ucode"}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(Signals{ExtraExtensions: []string{"siderolabs/intel-ucode", "siderolabs/gvisor"}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("schematic is order-dependent:\n%s\nvs\n%s", a, b)
	}
}
