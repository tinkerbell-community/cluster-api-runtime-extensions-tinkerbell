package resolve

import (
	"fmt"
	"strings"
)

// CustomizationConfig carries deploy-level inputs the resolver folds into every schematic.
type CustomizationConfig struct {
	// TootlesUserDataURL is the fully assembled talos.config kernel-arg value
	// (http://<tinkerbell-ip>:7080/2009-04-04/user-data).
	TootlesUserDataURL string
}

// baseKernelArgs are the constant kernel args every metal node boots with (§3.2). They are
// constants, not derived: the consoles and net.ifnames=0 match terraform's images module.
var baseKernelArgs = []string{"console=tty0", "console=ttyAMA0,115200", "net.ifnames=0"}

// TootlesUserDataURL assembles the talos.config user-data URL from the deploy flags. One of
// tootlesURL (full base, e.g. http://10.0.0.1:7080) or tinkerbellIP is REQUIRED; without the
// talos.config kernel arg a raw-disk node boots into maintenance mode and provisioning hangs.
func TootlesUserDataURL(tootlesURL, tinkerbellIP string) (string, error) {
	base := strings.TrimSuffix(tootlesURL, "/")
	if base == "" && tinkerbellIP != "" {
		base = fmt.Sprintf("http://%s:7080", tinkerbellIP)
	}
	if base == "" {
		return "", fmt.Errorf("one of --tootles-url or --tinkerbell-ip is required to build talos.config")
	}
	return base + "/2009-04-04/user-data", nil
}

// assembleKernelArgs builds the ordered, deduplicated kernel-arg list: the constants, then
// talos.config, then C1's extra-kernel-args. Order is fixed so the content hash is stable.
func assembleKernelArgs(tootlesUserDataURL, extraKernelArgsAnnotation string) []string {
	ordered := make([]string, 0, len(baseKernelArgs)+2)
	ordered = append(ordered, baseKernelArgs...)
	if tootlesUserDataURL != "" {
		ordered = append(ordered, "talos.config="+tootlesUserDataURL)
	}
	ordered = append(ordered, splitCSV(extraKernelArgsAnnotation)...)

	seen := map[string]struct{}{}
	out := make([]string, 0, len(ordered))
	for _, arg := range ordered {
		if _, ok := seen[arg]; ok {
			continue
		}
		seen[arg] = struct{}{}
		out = append(out, arg)
	}
	return out
}

// parseOverlay parses the overlay annotation ("name" or "name@image") into an Overlay, or
// nil when the annotation is absent/empty.
func parseOverlay(value string) *Overlay {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	name, image, _ := strings.Cut(value, "@")
	return &Overlay{Name: strings.TrimSpace(name), Image: strings.TrimSpace(image)}
}

// bootloaderFor returns the Image Factory bootloader for an architecture: arm64 boots via
// sd-boot; amd64 uses the Factory default (empty).
func bootloaderFor(arch string) string {
	if arch == archARM64 {
		return "sd-boot"
	}
	return ""
}

// OSSlug returns operating_system.os_slug in the authoritative DASH form talos-<version>-<arch>,
// e.g. talos-v1.13.9-amd64. The Template extracts arch via splitList "-" | last, which is why
// the underscore form (talos_v1_13_9) is never produced — it has no dashes and omits the slug.
func OSSlug(version, arch string) string {
	return fmt.Sprintf("talos-%s-%s", version, arch)
}

// splitCSV splits a comma-separated annotation, trimming blanks.
func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
