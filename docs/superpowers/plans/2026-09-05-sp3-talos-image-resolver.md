# SP-3: Extract the Talos Image-Factory Resolver — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract CAPT's Talos Image-Factory schematic/version engine into a new `internal/resolve` package plus a `talos-image-resolver` controller in the runtime-extensions repo, running in COEXISTENCE with CAPT (same engine/inputs, different write target), which resolves a per-machine schematic + Talos version and writes the image identity onto `Hardware.spec.metadata.instance.operating_system` (sparse SSA, field manager `talos-image-resolver`) plus a `talos.tinkerbell.org/installer-image` annotation.

**Architecture:** A pure, network-isolated engine (`internal/resolve`) ports CAPT's `pkg/schematic` and version policy 1:1, then grows a P2 `Customization` (`ExtraKernelArgs`, `Overlay`, `Bootloader`) so one content-addressed schematic ID backs both a bootable raw image and a correct installer image. A leader-elected controller reconciles `TinkerbellMachine`, reaches the concrete Talos version through the owning `Machine`'s bootstrap `TalosConfig`, gates on the claimed-Hardware predicate, and does a sparse server-side apply of only identity + the `operating_system` path (+ two annotations), freezing the block on provisioned Hardware while still refreshing the installer-image annotation. SP-3 stops at "the resolver writes the Hardware attributes" — it does NOT ship the Workflow Template or the Workflow-CREATE webhook (SP-4).

**Tech Stack:** Go 1.26.3; `sigs.k8s.io/controller-runtime` v0.24.1; `sigs.k8s.io/cluster-api` v1.13.0 (core `api/core/v1beta2`); `github.com/tinkerbell/tinkerbell/api` v0.25.0 (`tinkv1` = `.../v1alpha1/tinkerbell`); `sigs.k8s.io/yaml` v1.6.0; slog via `internal/logging`; Prometheus via controller-runtime metrics registry; envtest (K8s 1.37.0).

**Spec:** `docs/runtime-extensions-migration.md` §3 (§3.1 what moves, §3.2 the two-resolver P2 problem, §3.3 exact field map, §3.4 inputs + version policy, §3.5 SSA discipline + triggers + provisioned-freeze). Read §3 alongside this plan; every task argues from it.

## Global Constraints

- **Module path:** `github.com/tinkerbell-community/tinkerbell-bmc-discovery-controller` (import `internal/resolve` as `.../internal/resolve`).
- **Go version:** `go 1.26.3` (from `go.mod`).
- **CAPI:** `sigs.k8s.io/cluster-api v1.13.0`; core Machine type is `clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"`; `machine.Spec.Bootstrap.ConfigRef` is a `ContractVersionedObjectReference{Kind, Name, APIGroup}` with `.IsDefined()`.
- **controller-runtime:** `sigs.k8s.io/controller-runtime v0.24.1`.
- **Tinkerbell API:** `github.com/tinkerbell/tinkerbell/api v0.25.0`; import `tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"`; Hardware GVK is `tinkerbell.org/v1alpha1`, kind `Hardware`; the write target is `tinkv1.MetadataInstanceOperatingSystem{Slug, Distro, Version, ImageTag, OsSlug}` at `spec.metadata.instance.operating_system`.
- **YAML:** `sigs.k8s.io/yaml v1.6.0` (schematic marshaling; the engine pulls no new deps beyond `tinkv1` + this).
- **Strict lint MUST pass:** `make lint` (`golangci-lint run`, config v2, `.golangci.yml`). Every exported identifier carries a doc comment starting with its name (revive `exported`); package files carry a package comment (`package-comments`); error strings are lowercase, no trailing punctuation (`error-strings`); `context.Context` is the first arg (`context-as-argument`); functions stay under cyclop 20 / gocognit; no `dupl` over 200 tokens in non-test code; `goconst` for any literal used ≥5 times of len ≥4. Test files are exempt from `dupl`/`errcheck`/`forcetypeassert`/`gosec`/`noctx`/`cyclop`/`goconst`.
- **SSA field-manager discipline:** writes use `client.Apply(ctx, client.ApplyConfigurationFromUnstructured(u), client.FieldOwner(FieldManager), client.ForceOwnership)` on a hand-built sparse `unstructured.Unstructured` carrying only identity + the target path + granular annotation keys, with the read `resourceVersion` set as an optimistic precondition. Field manager string is `talos-image-resolver` (a string, distinct per §2.6 even in one process). NEVER assert `spec.userData`, `spec.disks`, `instance.id`, `instance.hostname`, or C1's annotations.
- **Logging:** `internal/logging` — `logging.New(level, format, w)` builds the root slog logger; `logging.Component(root, name)` derives named loggers; `ctrl.SetLogger(logr.FromSlogHandler(root.Handler()))` bridges controller-runtime; inside `Reconcile` use `logf.FromContext(ctx)`.
- **Tests:** `CGO_ENABLED=1 go test -race ./...` (unit); `make test-envtest` runs `-tags envtest -run 'TestEnvtest'` with `KUBEBUILDER_ASSETS` from setup-envtest (K8s 1.37.0). All tests run under `-race`.
- **RBAC MUST grant `get talosconfigs.bootstrap.cluster.x-k8s.io`** — a `Forbidden` there silently disables resolution (architecture.md P3, spec §3.4).
- **Process:** TDD (failing test first), frequent commits (one per task minimum), no product code outside a red→green cycle.

## File Structure

| File | Responsibility |
| --- | --- |
| `internal/resolve/resolve.go` | Package doc + all shared constants: `Name`, `FieldManager`, annotation/label keys (`ExtensionsAnnotation`, `ContractAnnotation`, `OverlayAnnotation`, `ExtraKernelArgsAnnotation`, `InstallerImageAnnotation`, `OwnerNameLabel`, `OwnerNamespaceLabel`, `ProvisionedAnnotation`), `DefaultFactoryURL`, `distro`, `nvmeCLIExtension`, arch names; `IsProvisioned(hw)`. |
| `internal/resolve/schematic.go` | `Schematic`/`Customization`(+P2 fields)/`SystemExtensions`/`Overlay`/`Signals` types; `Build(Signals) Schematic` (dedupe/sort + NVMe rule + pass-through P2); `Marshal()`; `InstallerImage()`; `registryHost()`; `hasNVMeDisk()`. |
| `internal/resolve/registry.go` | `Registrar` — content-hash cache + Factory `POST /schematics` (ported 1:1). |
| `internal/resolve/versions.go` | `VersionResolver` — `LatestPatch`/`LatestMinor`, 10-min TTL cache, lock-across-fetch single-flight, GA-only parse, stale-on-error (ported 1:1). |
| `internal/resolve/customization.go` | P2 assembly + field-map helpers: kernel-arg constants, `assembleKernelArgs`, `parseOverlay`, `bootloaderFor`, `OSSlug`, `TootlesUserDataURL`, `CustomizationConfig`. |
| `internal/resolve/signals.go` | `SignalsFromHardware(hw, extraFromMachine, cfg) Signals`; `architectureOf`; `parseExtensions`; disk collection; reads C1 annotations. |
| `internal/resolve/version_policy.go` | `VersionPolicy.Resolve` (full pin / bare minor / contract-pin), `contractMinor`, `SpecTalosVersion` (Machine→configRef→TalosConfig unstructured), `MachineExtensions`, `fullTalosVersion` regex. |
| `internal/resolve/reconciler.go` | `Reconciler` + `Reconcile` + `isClaimed` predicate + `resolve()` (pure decision → `Plan`) + provisioned-freeze + `apply()` (sparse SSA) + `ownerMachine` + `Plan` type + GVKs. |
| `internal/resolve/setup.go` | `SetupWithManager` (primary `TinkerbellMachine` unstructured; `Watches` on Hardware + bootstrap TalosConfig) + mapping funcs. |
| `internal/resolve/metrics.go` | Prometheus counters registered on the controller-runtime registry. |
| `internal/resolve/*_test.go` | Unit tests per file (fake client + httptest factory). |
| `internal/resolve/envtest_test.go` | `//go:build envtest`, `package resolve` — managed-fields SSA ownership, coexistence transfer, optimistic-concurrency conflict against a real API server. |
| `cmd/talos-image-resolver/main.go` | Manager wiring: flags, scheme, `Registrar`/`VersionResolver`/`VersionPolicy`, `Reconciler`, leader election, health/metrics. |
| `helm/talos-image-resolver/{Chart.yaml,values.yaml,templates/{_helpers.tpl,serviceaccount.yaml,rbac.yaml,deployment.yaml}}` | Deployable chart; RBAC includes `get talosconfigs`. |
| `Makefile` (modify) | Add `./internal/resolve/...` to the `test-envtest` target. |

---

### Task 1: Schematic document + Build engine (with P2 Customization)

**Files:**
- Create: `internal/resolve/resolve.go`
- Create: `internal/resolve/schematic.go`
- Test: `internal/resolve/schematic_test.go`

**Interfaces:**
- Consumes: `tinkv1` Hardware (`hw.Spec.Disks[].Device`).
- Produces:
  - `resolve.Signals struct { Architecture string; DiskDevices []string; ExtraExtensions []string; ExtraKernelArgs []string; Overlay *Overlay; Bootloader string }`
  - `resolve.Overlay struct { Name string; Image string }` (JSON `name`,`image`)
  - `resolve.Customization struct { SystemExtensions SystemExtensions; ExtraKernelArgs []string; Overlay *Overlay; Bootloader string }`
  - `resolve.SystemExtensions struct { OfficialExtensions []string }`
  - `resolve.Schematic struct { Customization Customization }`
  - `func Build(signals Signals) Schematic`
  - `func (s Schematic) Marshal() ([]byte, error)`
  - `func InstallerImage(factoryURL, id, talosVersion string) string`
  - Constants in `resolve.go`: `Name = "talos-image-resolver"`, `FieldManager = Name`, `DefaultFactoryURL = "https://factory.talos.dev"`, `ExtensionsAnnotation`, `ContractAnnotation`, `OverlayAnnotation`, `ExtraKernelArgsAnnotation`, `InstallerImageAnnotation`, `OwnerNameLabel`, `OwnerNamespaceLabel`, `ProvisionedAnnotation`, `distro = "talos"`, `nvmeCLIExtension`, `archAMD64`/`archARM64`; `func IsProvisioned(hw *tinkv1.Hardware) bool`.

- [ ] **Step 1: Write the failing test**

Create `internal/resolve/schematic_test.go`:

```go
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
	for _, want := range []string{"extraKernelArgs", "talos.config=http://10.0.0.1:7080/2009-04-04/user-data",
		"console=tty0", "net.ifnames=0", "overlay", "rpi_generic", "siderolabs/sbc-raspberrypi", "bootloader", "sd-boot"} {
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=1 go test -race ./internal/resolve/...`
Expected: FAIL — `undefined: Build`, `undefined: Signals`, etc. (package does not compile).

- [ ] **Step 3: Write minimal implementation**

Create `internal/resolve/resolve.go`:

```go
// Package resolve resolves a per-machine Talos Image Factory schematic and Talos
// version and writes the resulting image identity onto claimed Tinkerbell Hardware.
//
// It ports cluster-api-provider-tinkerbell's pkg/schematic engine and version policy
// unchanged, extends the schematic Customization with the bootable-image inputs the
// Factory honors for the raw disk (§3.2), and, unlike CAPT, writes the identity onto
// Hardware.spec.metadata.instance.operating_system by sparse server-side apply rather
// than into a provider status. See docs/runtime-extensions-migration.md §3.
package resolve

import tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"

const (
	// Name is the component name: binary, chart, image, field owner, event recorder.
	Name = "talos-image-resolver"

	// FieldManager attributes the resolver's server-side applies in managedFields.
	// A field manager is a string, not a process: it stays distinct even though this
	// logic is folded into one manager binary (spec §2.6).
	FieldManager = Name

	// DefaultFactoryURL is the public Image Factory.
	DefaultFactoryURL = "https://factory.talos.dev"

	// ExtensionsAnnotation lists official system extensions to include (comma separated).
	// C1 is the exclusive writer of this key on Hardware; TinkerbellMachine may also carry it.
	ExtensionsAnnotation = "talos.tinkerbell.org/system-extensions"

	// ContractAnnotation pins the Talos minor a machine tracks, e.g. "v1.14". The resolver
	// stamps it on first resolution of an unpinned, unprovisioned machine and owns it.
	ContractAnnotation = "talos.tinkerbell.org/contract"

	// OverlayAnnotation carries the Image Factory overlay (C1-written). Value is "name" or
	// "name@image", e.g. "rpi_generic@siderolabs/sbc-raspberrypi".
	OverlayAnnotation = "talos.tinkerbell.org/overlay"

	// ExtraKernelArgsAnnotation carries additional kernel args (C1-written, comma separated).
	ExtraKernelArgsAnnotation = "talos.tinkerbell.org/extra-kernel-args"

	// InstallerImageAnnotation publishes the upgrade-rendezvous installer reference; resolver-owned.
	InstallerImageAnnotation = "talos.tinkerbell.org/installer-image"

	// OwnerNameLabel / OwnerNamespaceLabel are CAPT's claim markers, set to the
	// TinkerbellMachine's name/namespace at claim and removed at release.
	OwnerNameLabel      = "v1alpha1.tinkerbell.org/ownerName"
	OwnerNamespaceLabel = "v1alpha1.tinkerbell.org/ownerNamespace"

	// ProvisionedAnnotation is stamped by CAPT on workflow success and drives the
	// provisioned-freeze split (§3.5).
	ProvisionedAnnotation = "v1alpha1.tinkerbell.org/provisioned"

	// distro is the constant operating_system.distro value.
	distro = "talos"

	// nvmeCLIExtension is the NVMe userspace tooling built in when an NVMe disk is present.
	nvmeCLIExtension = "siderolabs/nvme-cli"

	archAMD64 = "amd64"
	archARM64 = "arm64"
)

// IsProvisioned reports whether CAPT has finished provisioning the Hardware.
func IsProvisioned(hw *tinkv1.Hardware) bool {
	_, ok := hw.GetAnnotations()[ProvisionedAnnotation]
	return ok
}
```

Create `internal/resolve/schematic.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=1 go test -race ./internal/resolve/...`
Expected: PASS (4 tests).

- [ ] **Step 5: Lint + commit**

Run: `golangci-lint run ./internal/resolve/...`
Expected: no findings.

```bash
git add internal/resolve/resolve.go internal/resolve/schematic.go internal/resolve/schematic_test.go
git commit -m "feat(resolve): schematic document + Build engine with P2 customization"
```

---

### Task 2: Registrar — Factory registration + content-hash cache

**Files:**
- Create: `internal/resolve/registry.go`
- Test: `internal/resolve/registry_test.go`

**Interfaces:**
- Consumes: `resolve.Schematic`, `(Schematic).Marshal()` (Task 1).
- Produces:
  - `func NewRegistrar(factoryURL string) *Registrar`
  - `func (r *Registrar) Register(ctx context.Context, schematic Schematic) (string, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/resolve/registry_test.go`:

```go
package resolve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterUploadsOnceAndCaches(t *testing.T) {
	t.Parallel()
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/schematics" || r.Method != http.MethodPost {
			t.Errorf("got %s %s, want POST /schematics", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "abc123"})
	}))
	defer server.Close()

	registrar := NewRegistrar(server.URL)
	doc := Build(Signals{Architecture: archAMD64, DiskDevices: []string{"/dev/nvme0n1"}})
	for range 3 {
		id, err := registrar.Register(context.Background(), doc)
		if err != nil {
			t.Fatal(err)
		}
		if id != "abc123" {
			t.Fatalf("id = %q, want abc123", id)
		}
	}
	if calls != 1 {
		t.Errorf("uploaded %d times, want 1 — steady-state reconciles must not call the factory", calls)
	}
}

func TestRegisterReportsFactoryErrors(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid schematic"))
	}))
	defer server.Close()
	if _, err := NewRegistrar(server.URL).Register(context.Background(), Build(Signals{})); err == nil {
		t.Fatal("expected an error when the factory rejects the schematic")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=1 go test -race ./internal/resolve/... -run TestRegister`
Expected: FAIL — `undefined: NewRegistrar`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/resolve/registry.go` (ported 1:1 from CAPT `pkg/schematic/registry.go`, package renamed):

```go
package resolve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Registrar uploads schematics to an Image Factory and returns their IDs. Registration
// is idempotent (the factory returns the same ID for the same document), which is what
// makes the content-hash cache safe.
type Registrar struct {
	baseURL string
	client  *http.Client

	mu    sync.Mutex
	cache map[string]string
}

// NewRegistrar builds a Registrar against an Image Factory. An empty factoryURL selects
// the public factory.
func NewRegistrar(factoryURL string) *Registrar {
	if factoryURL == "" {
		factoryURL = DefaultFactoryURL
	}
	return &Registrar{
		baseURL: strings.TrimSuffix(factoryURL, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
		cache:   map[string]string{},
	}
}

// Register uploads a schematic and returns its ID, caching on the document's content hash.
func (r *Registrar) Register(ctx context.Context, schematic Schematic) (string, error) {
	body, err := schematic.Marshal()
	if err != nil {
		return "", err
	}
	key := contentKey(body)

	r.mu.Lock()
	cached, ok := r.cache[key]
	r.mu.Unlock()
	if ok {
		return cached, nil
	}

	id, err := r.upload(ctx, body)
	if err != nil {
		return "", err
	}

	r.mu.Lock()
	r.cache[key] = id
	r.mu.Unlock()
	return id, nil
}

func (r *Registrar) upload(ctx context.Context, body []byte) (string, error) {
	endpoint := r.baseURL + "/schematics"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("building schematic request: %w", err)
	}
	req.Header.Set("Content-Type", "application/yaml")

	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("uploading schematic to %s: %w", endpoint, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only response

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", fmt.Errorf("reading schematic response: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("image factory returned %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}

	var decoded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", fmt.Errorf("decoding schematic response: %w", err)
	}
	if decoded.ID == "" {
		return "", fmt.Errorf("image factory returned an empty schematic id")
	}
	return decoded.ID, nil
}

func contentKey(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=1 go test -race ./internal/resolve/... -run TestRegister`
Expected: PASS.

- [ ] **Step 5: Lint + commit**

```bash
git add internal/resolve/registry.go internal/resolve/registry_test.go
git commit -m "feat(resolve): port Factory Registrar with content-hash cache"
```

---

### Task 3: VersionResolver — versions listing, minor/patch policy, TTL cache

**Files:**
- Create: `internal/resolve/versions.go`
- Test: `internal/resolve/versions_test.go`

**Interfaces:**
- Consumes: `DefaultFactoryURL` (Task 1).
- Produces:
  - `func NewVersionResolver(factoryURL string) *VersionResolver`
  - `func (r *VersionResolver) LatestPatch(ctx context.Context, floor string) (string, error)`
  - `func (r *VersionResolver) LatestMinor(ctx context.Context) (string, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/resolve/versions_test.go`:

```go
package resolve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func realisticVersions() []string {
	return []string{
		"v1.12.10", "v1.12.11", "v1.12.12",
		"v1.13.0", "v1.13.2", "v1.13.9", "v1.13.10",
		"v1.14.0-alpha.0", "v1.14.0-beta.1", "v1.14.0-rc.2", "v1.14.0",
	}
}

func versionsServer(t *testing.T, hits *int32, versions []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		_ = json.NewEncoder(w).Encode(versions)
	}))
}

func TestLatestPatchReturnsNewestGAInMinor(t *testing.T) {
	t.Parallel()
	srv := versionsServer(t, nil, realisticVersions())
	defer srv.Close()
	got, err := NewVersionResolver(srv.URL).LatestPatch(context.Background(), "v1.13.9")
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.13.10" {
		t.Errorf("LatestPatch(v1.13.9) = %q, want v1.13.10", got)
	}
}

func TestLatestPatchSkipsPreReleases(t *testing.T) {
	t.Parallel()
	srv := versionsServer(t, nil, realisticVersions())
	defer srv.Close()
	got, err := NewVersionResolver(srv.URL).LatestPatch(context.Background(), "v1.14")
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.14.0" {
		t.Errorf("LatestPatch(v1.14) = %q, want v1.14.0 (pre-releases skipped)", got)
	}
}

func TestLatestPatchUnknownMinorReturnsEmpty(t *testing.T) {
	t.Parallel()
	srv := versionsServer(t, nil, realisticVersions())
	defer srv.Close()
	got, err := NewVersionResolver(srv.URL).LatestPatch(context.Background(), "v1.99")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("LatestPatch(v1.99) = %q, want empty", got)
	}
}

func TestLatestMinorIgnoresMinorWithOnlyPreReleases(t *testing.T) {
	t.Parallel()
	srv := versionsServer(t, nil, []string{"v1.13.9", "v1.13.10", "v1.14.0-alpha.0", "v1.14.0-rc.2"})
	defer srv.Close()
	got, err := NewVersionResolver(srv.URL).LatestMinor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.13" {
		t.Errorf("LatestMinor() = %q, want v1.13", got)
	}
}

func TestVersionsAreCachedWithinTTL(t *testing.T) {
	t.Parallel()
	var hits int32
	srv := versionsServer(t, &hits, realisticVersions())
	defer srv.Close()
	r := NewVersionResolver(srv.URL)
	for range 3 {
		if _, err := r.LatestPatch(context.Background(), "v1.13"); err != nil {
			t.Fatal(err)
		}
	}
	if hits != 1 {
		t.Errorf("factory hit %d times, want 1 (cached within TTL)", hits)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=1 go test -race ./internal/resolve/... -run 'TestLatest|TestVersions'`
Expected: FAIL — `undefined: NewVersionResolver`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/resolve/versions.go` (ported 1:1 from CAPT `pkg/schematic/versions.go`, package renamed):

```go
package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// versionsCacheTTL is how long a fetched /versions listing is trusted before refresh.
const versionsCacheTTL = 10 * time.Minute

// gaVersionRe matches a General Availability Talos version and rejects any pre-release.
var gaVersionRe = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// minorRe extracts major.minor from a floor written as a bare minor or a full version.
var minorRe = regexp.MustCompile(`^v(\d+)\.(\d+)`)

// VersionResolver answers "what is the newest Talos release" against an Image Factory.
type VersionResolver struct {
	baseURL string
	client  *http.Client
	ttl     time.Duration

	mu        sync.Mutex
	cache     []talosSemver
	fetchedAt time.Time
}

// NewVersionResolver builds a resolver against an Image Factory; empty selects the public one.
func NewVersionResolver(factoryURL string) *VersionResolver {
	if factoryURL == "" {
		factoryURL = DefaultFactoryURL
	}
	return &VersionResolver{
		baseURL: strings.TrimSuffix(factoryURL, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
		ttl:     versionsCacheTTL,
	}
}

// LatestPatch returns the newest GA patch within the minor of floor, or "" when the minor
// has no GA release (callers treat "" as "leave unresolved", not an error).
func (r *VersionResolver) LatestPatch(ctx context.Context, floor string) (string, error) {
	major, minor, ok := parseMinor(floor)
	if !ok {
		return "", nil
	}
	versions, err := r.versions(ctx)
	if err != nil {
		return "", err
	}
	var best talosSemver
	found := false
	for _, v := range versions {
		if v.major != major || v.minor != minor {
			continue
		}
		if !found || best.less(v) {
			best, found = v, true
		}
	}
	if !found {
		return "", nil
	}
	return best.String(), nil
}

// LatestMinor returns the newest GA minor line, skipping minors that are only pre-releases.
func (r *VersionResolver) LatestMinor(ctx context.Context) (string, error) {
	versions, err := r.versions(ctx)
	if err != nil {
		return "", err
	}
	best := talosSemver{}
	found := false
	for _, v := range versions {
		if !found || best.less(v) {
			best, found = v, true
		}
	}
	if !found {
		return "", nil
	}
	return fmt.Sprintf("v%d.%d", best.major, best.minor), nil
}

// versions returns the GA versions the factory can build, refreshing on TTL. The lock is
// held across the fetch so cold-cache reconciles coalesce onto one request (single-flight);
// a fetch error serves a stale listing rather than failing resolution outright.
func (r *VersionResolver) versions(ctx context.Context) ([]talosSemver, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache != nil && time.Since(r.fetchedAt) < r.ttl {
		return r.cache, nil
	}
	fetched, err := r.fetch(ctx)
	if err != nil {
		if r.cache != nil {
			return r.cache, nil
		}
		return nil, err
	}
	r.cache = fetched
	r.fetchedAt = time.Now()
	return r.cache, nil
}

func (r *VersionResolver) fetch(ctx context.Context) ([]talosSemver, error) {
	endpoint := r.baseURL + "/versions"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("building versions request: %w", err)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("listing versions from %s: %w", endpoint, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only response

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading versions response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image factory returned %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}
	var raw []string
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("decoding versions response: %w", err)
	}
	out := make([]talosSemver, 0, len(raw))
	for _, s := range raw {
		if v, ok := parseGAVersion(s); ok {
			out = append(out, v)
		}
	}
	return out, nil
}

type talosSemver struct{ major, minor, patch int }

func (v talosSemver) String() string { return fmt.Sprintf("v%d.%d.%d", v.major, v.minor, v.patch) }

func (v talosSemver) less(other talosSemver) bool {
	if v.major != other.major {
		return v.major < other.major
	}
	if v.minor != other.minor {
		return v.minor < other.minor
	}
	return v.patch < other.patch
}

func parseGAVersion(s string) (talosSemver, bool) {
	m := gaVersionRe.FindStringSubmatch(s)
	if m == nil {
		return talosSemver{}, false
	}
	return talosSemver{major: atoi(m[1]), minor: atoi(m[2]), patch: atoi(m[3])}, true
}

func parseMinor(floor string) (major, minor int, ok bool) {
	m := minorRe.FindStringSubmatch(floor)
	if m == nil {
		return 0, 0, false
	}
	return atoi(m[1]), atoi(m[2]), true
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=1 go test -race ./internal/resolve/... -run 'TestLatest|TestVersions'`
Expected: PASS.

- [ ] **Step 5: Lint + commit**

```bash
git add internal/resolve/versions.go internal/resolve/versions_test.go
git commit -m "feat(resolve): port VersionResolver (TTL cache, single-flight, GA-only)"
```

---

### Task 4: Signals assembly + P2 Customization inputs + os_slug

**Files:**
- Create: `internal/resolve/customization.go`
- Create: `internal/resolve/signals.go`
- Test: `internal/resolve/customization_test.go`
- Test: `internal/resolve/signals_test.go`

**Interfaces:**
- Consumes: `Signals`, `Overlay` (Task 1); `tinkv1.Hardware`.
- Produces:
  - `type CustomizationConfig struct { TootlesUserDataURL string }`
  - `func TootlesUserDataURL(tootlesURL, tinkerbellIP string) (string, error)`
  - `func SignalsFromHardware(hw *tinkv1.Hardware, extraFromMachine []string, cfg CustomizationConfig) Signals`
  - `func OSSlug(version, arch string) string`
  - `func architectureOf(hw *tinkv1.Hardware) string`

- [ ] **Step 1: Write the failing tests**

Create `internal/resolve/customization_test.go`:

```go
package resolve

import (
	"strings"
	"testing"
)

// §3.3: os_slug is the DASH form talos-<version>-<arch>; the underscore fallback must
// NEVER be produced, and arch is always the final dash segment even for pre-releases.
func TestOSSlugIsDashFormNeverUnderscore(t *testing.T) {
	t.Parallel()
	got := OSSlug("v1.13.9", "amd64")
	if got != "talos-v1.13.9-amd64" {
		t.Fatalf("OSSlug = %q, want talos-v1.13.9-amd64", got)
	}
	if strings.Contains(got, "_") || strings.Contains(got, "talos_v1_13_9") {
		t.Fatalf("OSSlug produced the dead underscore form: %q", got)
	}
}

func TestOSSlugArchIsLastDashSegmentForPreRelease(t *testing.T) {
	t.Parallel()
	got := OSSlug("v1.14.0-rc.1", "amd64")
	parts := strings.Split(got, "-")
	if last := parts[len(parts)-1]; last != "amd64" {
		t.Fatalf("splitList last of %q = %q, want amd64", got, last)
	}
}

func TestTootlesUserDataURLFromIP(t *testing.T) {
	t.Parallel()
	got, err := TootlesUserDataURL("", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://10.0.0.1:7080/2009-04-04/user-data" {
		t.Fatalf("url = %q", got)
	}
}

func TestTootlesUserDataURLRequiresOneSource(t *testing.T) {
	t.Parallel()
	if _, err := TootlesUserDataURL("", ""); err == nil {
		t.Fatal("expected an error when neither --tootles-url nor --tinkerbell-ip is set")
	}
}
```

Create `internal/resolve/signals_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=1 go test -race ./internal/resolve/... -run 'TestOSSlug|TestTootles|TestArchitecture|TestSignals|TestAmd64'`
Expected: FAIL — `undefined: OSSlug`, `undefined: SignalsFromHardware`, etc.

- [ ] **Step 3: Write minimal implementation**

Create `internal/resolve/customization.go`:

```go
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
```

Create `internal/resolve/signals.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race ./internal/resolve/...`
Expected: PASS (all tasks so far).

- [ ] **Step 5: Lint + commit**

```bash
git add internal/resolve/customization.go internal/resolve/signals.go internal/resolve/customization_test.go internal/resolve/signals_test.go
git commit -m "feat(resolve): signals assembly, P2 customization inputs, dash-form os_slug"
```

---

### Task 5: Version policy (full pin / minor-track / contract-pin) + bootstrap TalosConfig read

**Files:**
- Create: `internal/resolve/version_policy.go`
- Test: `internal/resolve/version_policy_test.go`

**Interfaces:**
- Consumes: `VersionResolver` (Task 3); `clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"`; `client.Reader`.
- Produces:
  - `type VersionPolicy struct { Versions *VersionResolver }`
  - `func (p VersionPolicy) Resolve(ctx context.Context, raw string, provisioned bool, existingPin string) (version, newPin string, err error)`
  - `func SpecTalosVersion(ctx context.Context, c client.Reader, machine *clusterv1.Machine) string`
  - `func MachineExtensions(annotations map[string]string) []string`

- [ ] **Step 1: Write the failing test**

Create `internal/resolve/version_policy_test.go`:

```go
package resolve

import (
	"context"
	"testing"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func policy(t *testing.T, versions []string) VersionPolicy {
	t.Helper()
	srv := versionsServer(t, nil, versions)
	t.Cleanup(srv.Close)
	return VersionPolicy{Versions: NewVersionResolver(srv.URL)}
}

func TestResolveFullPinUsedExactly(t *testing.T) {
	t.Parallel()
	v, pin, err := policy(t, realisticVersions()).Resolve(context.Background(), "v1.13.9", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if v != "v1.13.9" || pin != "" {
		t.Fatalf("Resolve(full pin) = (%q,%q), want (v1.13.9,\"\")", v, pin)
	}
}

func TestResolveBareMinorTracksLatestPatch(t *testing.T) {
	t.Parallel()
	v, _, err := policy(t, realisticVersions()).Resolve(context.Background(), "v1.13", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if v != "v1.13.10" {
		t.Fatalf("Resolve(v1.13) = %q, want v1.13.10", v)
	}
}

func TestResolveUnsetUnprovisionedPinsNewestMinor(t *testing.T) {
	t.Parallel()
	v, pin, err := policy(t, realisticVersions()).Resolve(context.Background(), "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if pin != "v1.14" || v != "v1.14.0" {
		t.Fatalf("Resolve(unset, unprovisioned) = (%q, pin %q), want (v1.14.0, v1.14)", v, pin)
	}
}

func TestResolveExistingPinSkipsNewLatestMinor(t *testing.T) {
	t.Parallel()
	v, pin, err := policy(t, realisticVersions()).Resolve(context.Background(), "", false, "v1.13")
	if err != nil {
		t.Fatal(err)
	}
	if pin != "" || v != "v1.13.10" {
		t.Fatalf("Resolve(unset, pinned v1.13) = (%q, newPin %q), want (v1.13.10, \"\")", v, pin)
	}
}

// A provisioned machine with no pin is left unresolved so the resolver can never ask CABPT
// to skip a minor (Talos forbids it).
func TestResolveProvisionedUnpinnedReturnsEmpty(t *testing.T) {
	t.Parallel()
	v, pin, err := policy(t, realisticVersions()).Resolve(context.Background(), "", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if v != "" || pin != "" {
		t.Fatalf("Resolve(unset, provisioned) = (%q,%q), want (\"\",\"\")", v, pin)
	}
}

func TestSpecTalosVersionReadsBootstrapConfigUnstructured(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = clusterv1.AddToScheme(scheme)

	tc := &unstructured.Unstructured{}
	tc.SetGroupVersionKind(schema.GroupVersionKind{Group: "bootstrap.cluster.x-k8s.io", Version: "v1beta1", Kind: "TalosConfig"})
	tc.SetNamespace("tinkerbell")
	tc.SetName("tc-1")
	_ = unstructured.SetNestedField(tc.Object, "v1.13.9", "spec", "talosVersion")

	machine := &clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "tinkerbell", Name: "m1"}}
	machine.Spec.Bootstrap.ConfigRef = clusterv1.ContractVersionedObjectReference{
		APIGroup: "bootstrap.cluster.x-k8s.io", Kind: "TalosConfig", Name: "tc-1",
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc).Build()
	if got := SpecTalosVersion(context.Background(), c, machine); got != "v1.13.9" {
		t.Fatalf("SpecTalosVersion = %q, want v1.13.9", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=1 go test -race ./internal/resolve/... -run 'TestResolve|TestSpecTalos'`
Expected: FAIL — `undefined: VersionPolicy`, `undefined: SpecTalosVersion`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/resolve/version_policy.go`:

```go
package resolve

import (
	"context"
	"regexp"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// fullTalosVersion matches a complete Talos version (v1.14.0 or v1.14.0-rc.1). A bare minor
// (v1.13) is a config contract, not an installable OS version, so it is deliberately rejected.
var fullTalosVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.\-]+)?$`)

// VersionPolicy turns a spec-declared Talos version (full / bare-minor / unset) into the
// concrete patch the machine installs and upgrades to. It ports CAPT's policy 1:1; only the
// contract-pin carrier changes (the resolver stamps the pin on Hardware, not TinkerbellMachine).
type VersionPolicy struct {
	Versions *VersionResolver
}

// Resolve returns the concrete version to use plus a contract minor to stamp (empty = none).
//
//   - A full pin (v1.13.9) is used exactly, never bumped.
//   - A bare minor (v1.13) tracks the newest GA patch in that minor.
//   - Unset / "latest" pins the newest GA minor on first resolution of an unprovisioned machine
//     (returned as newPin for the caller to stamp); an already-pinned machine tracks its pin.
//   - A provisioned, unpinned machine is left unresolved (""), never pinned, so the resolver can
//     never ask CABPT to skip a minor.
//
// An empty version means "not knowable" — the caller writes no operating_system.
func (p VersionPolicy) Resolve(ctx context.Context, raw string, provisioned bool, existingPin string) (version, newPin string, err error) {
	if fullTalosVersion.MatchString(raw) {
		return raw, "", nil
	}
	if p.Versions == nil {
		return "", "", nil
	}
	floor := raw
	if floor == "" || floor == "latest" {
		floor, newPin, err = p.contractMinor(ctx, provisioned, existingPin)
		if err != nil {
			return "", "", err
		}
	}
	resolved, err := p.Versions.LatestPatch(ctx, floor)
	if err != nil {
		return "", "", err
	}
	return resolved, newPin, nil
}

// contractMinor returns the minor an unpinned machine tracks, plus a newPin when a fresh pin
// is established. An already-pinned machine keeps its pin (newPin empty); a provisioned machine
// is never pinned (its running minor is unknown here).
func (p VersionPolicy) contractMinor(ctx context.Context, provisioned bool, existingPin string) (floor, newPin string, err error) {
	if existingPin != "" {
		return existingPin, "", nil
	}
	if provisioned {
		return "", "", nil
	}
	minor, err := p.Versions.LatestMinor(ctx)
	if err != nil {
		return "", "", err
	}
	if minor == "" {
		return "", "", nil
	}
	return minor, minor, nil
}

// SpecTalosVersion reads spec.talosVersion from the Machine's bootstrap config, unstructured on
// purpose (no bootstrap-provider import; any provider exposing spec.talosVersion works). An empty
// result means unset or not readable yet. RBAC MUST grant get on talosconfigs — a Forbidden here
// silently disables resolution (§3.4, architecture.md P3).
func SpecTalosVersion(ctx context.Context, c client.Reader, machine *clusterv1.Machine) string {
	if machine == nil || !machine.Spec.Bootstrap.ConfigRef.IsDefined() {
		return ""
	}
	ref := machine.Spec.Bootstrap.ConfigRef
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: ref.APIGroup, Version: "v1beta1", Kind: ref.Kind})

	key := types.NamespacedName{Namespace: machine.Namespace, Name: ref.Name}
	if err := c.Get(ctx, key, obj); err != nil {
		logf.FromContext(ctx).V(1).Info("could not read bootstrap config for Talos version", "error", err.Error())
		return ""
	}
	version, found, err := unstructured.NestedString(obj.Object, "spec", "talosVersion")
	if err != nil || !found {
		return ""
	}
	return version
}

// MachineExtensions reads extra system extensions requested on the TinkerbellMachine.
func MachineExtensions(annotations map[string]string) []string {
	return splitCSV(annotations[ExtensionsAnnotation])
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=1 go test -race ./internal/resolve/...`
Expected: PASS.

- [ ] **Step 5: Lint + commit**

```bash
git add internal/resolve/version_policy.go internal/resolve/version_policy_test.go
git commit -m "feat(resolve): version policy + bootstrap TalosConfig read (contract pin on Hardware)"
```

---

### Task 6: Reconciler — claimed predicate, provisioned-freeze, resolve decision, sparse SSA apply

**Files:**
- Create: `internal/resolve/reconciler.go`
- Create: `internal/resolve/metrics.go`
- Test: `internal/resolve/reconciler_test.go`

**Interfaces:**
- Consumes: everything above; `tinkv1`, `clusterv1`, `unstructured`, controller-runtime `client`, `events.EventRecorder`.
- Produces:
  - `var TinkerbellMachineGVK schema.GroupVersionKind` (`infrastructure.cluster.x-k8s.io/v1beta2`, `TinkerbellMachine`)
  - `type Plan struct { OperatingSystem *tinkv1.MetadataInstanceOperatingSystem; InstallerImage string; ContractPin string }`
  - `type Reconciler struct { client.Client; Recorder events.EventRecorder; Registrar *Registrar; Policy VersionPolicy; Customization CustomizationConfig; FactoryURL string }`
  - `func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error)`
  - `func isClaimed(hw *tinkv1.Hardware, tm *unstructured.Unstructured) bool`
  - `func (r *Reconciler) resolve(ctx context.Context, tm *unstructured.Unstructured, hw *tinkv1.Hardware, rawVersion string) (*Plan, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/resolve/reconciler_test.go`:

```go
package resolve

import (
	"context"
	"testing"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func tinkerbellMachine(name string, annotations map[string]string) *unstructured.Unstructured {
	tm := &unstructured.Unstructured{}
	tm.SetGroupVersionKind(TinkerbellMachineGVK)
	tm.SetNamespace("tinkerbell")
	tm.SetName(name)
	tm.SetAnnotations(annotations)
	_ = unstructured.SetNestedField(tm.Object, "hw-1", "spec", "hardwareName")
	return tm
}

func claimedBy(tm *unstructured.Unstructured, annotations map[string]string, arch string) *tinkv1.Hardware {
	hw := &tinkv1.Hardware{ObjectMeta: metav1.ObjectMeta{
		Name: "hw-1", Namespace: "tinkerbell",
		Labels:      map[string]string{OwnerNameLabel: tm.GetName(), OwnerNamespaceLabel: tm.GetNamespace()},
		Annotations: annotations,
	}}
	hw.Spec.Interfaces = []tinkv1.Interface{{DHCP: &tinkv1.DHCP{Arch: arch}}}
	return hw
}

func testReconciler(t *testing.T) *Reconciler {
	t.Helper()
	rsrv := versionsServer(t, nil, realisticVersions())
	t.Cleanup(rsrv.Close)
	factory := "http://factory.example.test"
	return &Reconciler{
		Registrar:     stubRegistrar(t, "sid123"),
		Policy:        VersionPolicy{Versions: NewVersionResolver(rsrv.URL)},
		Customization: CustomizationConfig{TootlesUserDataURL: testTootles},
		FactoryURL:    factory,
	}
}

func TestIsClaimedPredicate(t *testing.T) {
	t.Parallel()
	tm := tinkerbellMachine("m1", nil)
	if !isClaimed(claimedBy(tm, nil, "x86_64"), tm) {
		t.Error("owner labels matching the machine must be claimed")
	}
	unclaimed := claimedBy(tm, nil, "x86_64")
	unclaimed.Labels = nil
	if isClaimed(unclaimed, tm) {
		t.Error("hardware with no owner labels must not be claimed")
	}
	wrong := claimedBy(tm, nil, "x86_64")
	wrong.Labels[OwnerNameLabel] = "someone-else"
	if isClaimed(wrong, tm) {
		t.Error("hardware owned by another machine must not be claimed")
	}
}

func TestResolveUnprovisionedWritesFullBlock(t *testing.T) {
	t.Parallel()
	r := testReconciler(t)
	tm := tinkerbellMachine("m1", nil)
	hw := claimedBy(tm, nil, "x86_64")

	plan, err := r.resolve(context.Background(), tm, hw, "v1.13.9")
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil || plan.OperatingSystem == nil {
		t.Fatal("unprovisioned claimed hardware must get an operating_system block")
	}
	os := plan.OperatingSystem
	if os.Slug != "sid123" || os.Version != "v1.13.9" || os.ImageTag != "v1.13.9" || os.Distro != "talos" || os.OsSlug != "talos-v1.13.9-amd64" {
		t.Errorf("operating_system = %+v", os)
	}
	if plan.InstallerImage != "factory.example.test/metal-installer/sid123:v1.13.9" {
		t.Errorf("installer = %q", plan.InstallerImage)
	}
}

// §3.5 provisioned-freeze: do NOT recompute operating_system, but DO refresh installer-image.
func TestResolveProvisionedFreezesOSButRefreshesInstaller(t *testing.T) {
	t.Parallel()
	r := testReconciler(t)
	tm := tinkerbellMachine("m1", nil)
	hw := claimedBy(tm, map[string]string{ProvisionedAnnotation: "true"}, "x86_64")

	plan, err := r.resolve(context.Background(), tm, hw, "v1.13.9")
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil || plan.OperatingSystem != nil {
		t.Fatal("provisioned hardware must not get a recomputed operating_system block")
	}
	if plan.InstallerImage == "" {
		t.Error("provisioned hardware must still refresh the installer-image annotation")
	}
}

// §3.4 empty result ⇒ write nothing.
func TestResolveEmptyVersionWritesNothing(t *testing.T) {
	t.Parallel()
	r := testReconciler(t)
	tm := tinkerbellMachine("m1", nil)
	hw := claimedBy(tm, map[string]string{ProvisionedAnnotation: "true"}, "x86_64")

	plan, err := r.resolve(context.Background(), tm, hw, "") // provisioned + unpinned ⇒ ""
	if err != nil {
		t.Fatal(err)
	}
	if plan != nil {
		t.Fatalf("expected no plan, got %+v", plan)
	}
}
```

Add the registrar stub helper to `reconciler_test.go` (httptest-backed so `Register` returns a fixed id):

```go
func stubRegistrar(t *testing.T, id string) *Registrar {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	}))
	t.Cleanup(srv.Close)
	return NewRegistrar(srv.URL)
}
```

(Add imports `net/http`, `net/http/httptest`, `encoding/json` to the test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=1 go test -race ./internal/resolve/... -run 'TestIsClaimed|TestResolve'`
Expected: FAIL — `undefined: TinkerbellMachineGVK`, `undefined: isClaimed`, `undefined: (*Reconciler).resolve`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/resolve/metrics.go`:

```go
package resolve

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	identitiesResolved = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "resolver_identities_resolved_total",
		Help: "Image identities applied onto claimed Hardware.",
	})
	versionsUnresolved = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "resolver_versions_unresolved_total",
		Help: "Reconciles that resolved no Talos version and wrote no operating_system.",
	})
	applyConflicts = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "resolver_apply_conflicts_total",
		Help: "Sparse applies rejected by the resourceVersion precondition (concurrent write or release).",
	})
)

func init() {
	metrics.Registry.MustRegister(identitiesResolved, versionsUnresolved, applyConflicts)
}
```

Create `internal/resolve/reconciler.go`:

```go
package resolve

import (
	"context"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// TinkerbellMachineGVK identifies the infra machine, read as unstructured so the resolver
// imports no CAPT fork api (the teardown precedent).
var TinkerbellMachineGVK = schema.GroupVersionKind{
	Group:   "infrastructure.cluster.x-k8s.io",
	Version: "v1beta2",
	Kind:    "TinkerbellMachine",
}

// EventIdentityResolved is emitted on Hardware when the image identity is applied.
const EventIdentityResolved = "ImageIdentityResolved"

// Plan is the decision the resolver reached for one Hardware: which fields to apply. A nil
// field means "do not write it".
type Plan struct {
	// OperatingSystem is the identity block; nil under provisioned-freeze or when no version resolved.
	OperatingSystem *tinkv1.MetadataInstanceOperatingSystem
	// InstallerImage is the resolver-owned installer-image annotation value.
	InstallerImage string
	// ContractPin is a newly established talos.tinkerbell.org/contract minor to stamp.
	ContractPin string
}

func (p *Plan) empty() bool {
	return p == nil || (p.OperatingSystem == nil && p.InstallerImage == "" && p.ContractPin == "")
}

// Reconciler resolves the per-machine schematic + Talos version and writes the image identity
// onto claimed Hardware by sparse server-side apply (field manager talos-image-resolver). It
// coexists with CAPT, which still resolves into its own status: same engine and inputs, different
// write target (§3.5).
type Reconciler struct {
	client.Client
	Recorder      events.EventRecorder
	Registrar     *Registrar
	Policy        VersionPolicy
	Customization CustomizationConfig
	// FactoryURL is the resolved factory host used to compose the installer reference.
	FactoryURL string
}

// Reconcile resolves one TinkerbellMachine's claimed Hardware.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	tm := &unstructured.Unstructured{}
	tm.SetGroupVersionKind(TinkerbellMachineGVK)
	if err := r.Get(ctx, req.NamespacedName, tm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	hardwareName, _, _ := unstructured.NestedString(tm.Object, "spec", "hardwareName")
	if hardwareName == "" {
		return ctrl.Result{}, nil // not yet claimed to a Hardware
	}
	namespace, _, _ := unstructured.NestedString(tm.Object, "status", "targetNamespace")
	if namespace == "" {
		namespace = tm.GetNamespace()
	}

	hw := &tinkv1.Hardware{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: hardwareName}, hw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !isClaimed(hw, tm) {
		// Released or claimed by another machine: never our write (§3.5). Stop.
		return ctrl.Result{}, nil
	}

	machine, err := r.ownerMachine(ctx, tm)
	if err != nil {
		return ctrl.Result{}, err
	}
	raw := SpecTalosVersion(ctx, r.Client, machine)

	plan, err := r.resolve(ctx, tm, hw, raw)
	if err != nil {
		return ctrl.Result{}, err
	}
	if plan.empty() {
		versionsUnresolved.Inc()
		return ctrl.Result{}, nil
	}

	if err := r.apply(ctx, hw, plan); err != nil {
		if apierrors.IsConflict(err) {
			applyConflicts.Inc()
			log.V(1).Info("apply conflicted with a concurrent write; requeueing", "hardware", hw.Name)
		}
		return ctrl.Result{}, err
	}
	if plan.OperatingSystem != nil {
		identitiesResolved.Inc()
		r.Recorder.Eventf(hw, nil, corev1.EventTypeNormal, EventIdentityResolved, EventIdentityResolved,
			"resolved image identity: slug=%s version=%s", plan.OperatingSystem.Slug, plan.OperatingSystem.Version)
		log.Info("resolved image identity", "hardware", hw.Name, "slug", plan.OperatingSystem.Slug, "version", plan.OperatingSystem.Version)
	}
	return ctrl.Result{}, nil
}

// isClaimed verifies CAPT's claim: the Hardware's owner labels name this TinkerbellMachine.
// Load-bearing — the resolver never writes released or unclaimed Hardware (§3.5).
func isClaimed(hw *tinkv1.Hardware, tm *unstructured.Unstructured) bool {
	return hw.Labels[OwnerNameLabel] == tm.GetName() &&
		hw.Labels[OwnerNamespaceLabel] == tm.GetNamespace()
}

// resolve computes the Plan for a claimed Hardware. Provisioned Hardware freezes its
// operating_system (leaves the provisioning-time value) but still refreshes the installer-image
// annotation so the upgrade path stays current; an unresolvable version writes nothing (§3.4/§3.5).
func (r *Reconciler) resolve(ctx context.Context, tm *unstructured.Unstructured, hw *tinkv1.Hardware, rawVersion string) (*Plan, error) {
	provisioned := IsProvisioned(hw)
	version, newPin, err := r.Policy.Resolve(ctx, rawVersion, provisioned, hw.GetAnnotations()[ContractAnnotation])
	if err != nil {
		return nil, err
	}
	if version == "" {
		if newPin == "" {
			return nil, nil
		}
		return &Plan{ContractPin: newPin}, nil
	}

	signals := SignalsFromHardware(hw, MachineExtensions(tm.GetAnnotations()), r.Customization)
	id, err := r.Registrar.Register(ctx, Build(signals))
	if err != nil {
		return nil, err
	}

	plan := &Plan{
		InstallerImage: InstallerImage(r.FactoryURL, id, version),
		ContractPin:    newPin,
	}
	if !provisioned {
		plan.OperatingSystem = &tinkv1.MetadataInstanceOperatingSystem{
			Slug:     id,
			Distro:   distro,
			Version:  version,
			ImageTag: version,
			OsSlug:   OSSlug(version, signals.Architecture),
		}
	}
	return plan, nil
}

// ownerMachine returns the owning core Machine, or nil when none is set / found.
func (r *Reconciler) ownerMachine(ctx context.Context, tm *unstructured.Unstructured) (*clusterv1.Machine, error) {
	for _, ref := range tm.GetOwnerReferences() {
		if ref.Kind != "Machine" {
			continue
		}
		machine := &clusterv1.Machine{}
		err := r.Get(ctx, client.ObjectKey{Namespace: tm.GetNamespace(), Name: ref.Name}, machine)
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return machine, nil
	}
	return nil, nil
}

// apply writes the Plan onto the Hardware as a sparse server-side apply carrying only identity,
// the operating_system path, and the resolver's granular annotation keys — never userData, disks,
// instance.id/hostname, or C1's annotations (§3.5). The read resourceVersion is the optimistic
// precondition; ForceOwnership makes the coexistence handoff from talos-os-metadata deterministic.
func (r *Reconciler) apply(ctx context.Context, hw *tinkv1.Hardware, plan *Plan) error {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tinkerbell.org/v1alpha1",
		"kind":       "Hardware",
		"metadata": map[string]any{
			"name":            hw.Name,
			"namespace":       hw.Namespace,
			"resourceVersion": hw.ResourceVersion,
		},
	}}

	annotations := map[string]any{}
	if plan.InstallerImage != "" {
		annotations[InstallerImageAnnotation] = plan.InstallerImage
	}
	if plan.ContractPin != "" {
		annotations[ContractAnnotation] = plan.ContractPin
	}
	if len(annotations) > 0 {
		if err := unstructured.SetNestedMap(u.Object, annotations, "metadata", "annotations"); err != nil {
			return err
		}
	}
	if plan.OperatingSystem != nil {
		os := plan.OperatingSystem
		if err := unstructured.SetNestedMap(u.Object, map[string]any{
			"slug":      os.Slug,
			"distro":    os.Distro,
			"version":   os.Version,
			"image_tag": os.ImageTag,
			"os_slug":   os.OsSlug,
		}, "spec", "metadata", "instance", "operating_system"); err != nil {
			return err
		}
	}

	return r.Apply(ctx, client.ApplyConfigurationFromUnstructured(u),
		client.FieldOwner(FieldManager), client.ForceOwnership)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=1 go test -race ./internal/resolve/...`
Expected: PASS (`resolve()` decision + `isClaimed`; the SSA write is asserted in Task 7 envtest).

- [ ] **Step 5: Lint + commit**

```bash
git add internal/resolve/reconciler.go internal/resolve/metrics.go internal/resolve/reconciler_test.go
git commit -m "feat(resolve): reconciler decision — claimed predicate, provisioned-freeze, sparse SSA plan"
```

---

### Task 7: Controller wiring — SetupWithManager, watches, cmd, helm, RBAC, envtest

**Files:**
- Create: `internal/resolve/setup.go`
- Create: `cmd/talos-image-resolver/main.go`
- Create: `helm/talos-image-resolver/Chart.yaml`
- Create: `helm/talos-image-resolver/values.yaml`
- Create: `helm/talos-image-resolver/templates/_helpers.tpl`
- Create: `helm/talos-image-resolver/templates/serviceaccount.yaml`
- Create: `helm/talos-image-resolver/templates/rbac.yaml`
- Create: `helm/talos-image-resolver/templates/deployment.yaml`
- Modify: `Makefile` (add `./internal/resolve/...` to `test-envtest`)
- Test: `internal/resolve/envtest_test.go`

**Interfaces:**
- Consumes: `Reconciler` (Task 6), `TinkerbellMachineGVK`, claim label constants.
- Produces:
  - `func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, concurrency int) error`
  - `func hardwareToTinkerbellMachine(obj client.Object) []reconcile.Request`
  - `func (r *Reconciler) talosConfigToTinkerbellMachine(ctx context.Context, obj client.Object) []reconcile.Request`

- [ ] **Step 1: Write the failing envtest**

Create `internal/resolve/envtest_test.go`:

```go
//go:build envtest

package resolve

// The SSA managed-fields behavior — sole ownership of operating_system, coexistence handoff
// from the old talos-os-metadata manager, and the resourceVersion precondition — is exactly
// what the fake client fakes badly, so it runs against a real API server. Run: make test-envtest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var (
	envCfg    *rest.Config
	envClient client.Client
)

func TestMain(m *testing.M) {
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "test", "crds")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting envtest: %v\n", err)
		os.Exit(1)
	}
	envCfg = cfg
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(tinkv1.AddToScheme(scheme))
	envClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "building client: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = testEnv.Stop()
	os.Exit(code)
}

func newNamespace(t *testing.T) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "resolve-envtest-"}}
	if err := envClient.Create(context.Background(), ns); err != nil {
		t.Fatalf("creating test namespace: %v", err)
	}
	return ns.Name
}

// applyAs performs the resolver's sparse operating_system apply under an arbitrary field manager,
// standing in for the retired talos-os-metadata mirror in the coexistence test. It mirrors
// Reconciler.apply's unstructured construction so the managed-fields shape is identical.
func applyAs(ctx context.Context, t *testing.T, manager string, hw *tinkv1.Hardware, p *Plan) {
	t.Helper()
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tinkerbell.org/v1alpha1",
		"kind":       "Hardware",
		"metadata": map[string]any{
			"name":            hw.Name,
			"namespace":       hw.Namespace,
			"resourceVersion": hw.ResourceVersion,
		},
	}}
	os := p.OperatingSystem
	if err := unstructured.SetNestedMap(u.Object, map[string]any{
		"slug": os.Slug, "distro": os.Distro, "version": os.Version,
		"image_tag": os.ImageTag, "os_slug": os.OsSlug,
	}, "spec", "metadata", "instance", "operating_system"); err != nil {
		t.Fatalf("applyAs(%s) build: %v", manager, err)
	}
	if err := envClient.Apply(ctx, client.ApplyConfigurationFromUnstructured(u),
		client.FieldOwner(manager), client.ForceOwnership); err != nil {
		t.Fatalf("applyAs(%s): %v", manager, err)
	}
}

// assertOwns re-reads the Hardware and asserts the named field manager owns the given leaf field,
// by scanning that manager's managedFields entry for its `f:<field>` selector.
func assertOwns(t *testing.T, ctx context.Context, obj client.Object, manager, field string) {
	t.Helper()
	got := &tinkv1.Hardware{}
	if err := envClient.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
		t.Fatalf("re-getting %s: %v", obj.GetName(), err)
	}
	for _, mf := range got.GetManagedFields() {
		if mf.Manager != manager || mf.FieldsV1 == nil {
			continue
		}
		if strings.Contains(string(mf.FieldsV1.Raw), "f:"+field) {
			return
		}
	}
	t.Errorf("field manager %q does not own %q", manager, field)
}

func plan(id, version, installer string) *Plan {
	return &Plan{
		OperatingSystem: &tinkv1.MetadataInstanceOperatingSystem{
			Slug: id, Distro: distro, Version: version, ImageTag: version, OsSlug: OSSlug(version, "amd64"),
		},
		InstallerImage: installer,
	}
}

// TestEnvtestResolverOwnsOperatingSystemWithoutTouchingUserData asserts the sparse apply owns
// operating_system while leaving another manager's spec.userData untouched.
func TestEnvtestResolverOwnsOperatingSystemWithoutTouchingUserData(t *testing.T) {
	ns := newNamespace(t)
	ctx := context.Background()

	// Discovery-style manager authors userData.
	seed := &tinkv1.Hardware{ObjectMeta: metav1.ObjectMeta{
		Name: "hw-1", Namespace: ns,
		Labels: map[string]string{OwnerNameLabel: "m1", OwnerNamespaceLabel: ns},
	}}
	seed.Spec.UserData = ptr.To("#cloud-config machine config")
	if err := envClient.Create(ctx, seed, client.FieldOwner("discovery")); err != nil {
		t.Fatal(err)
	}

	live := &tinkv1.Hardware{}
	if err := envClient.Get(ctx, client.ObjectKeyFromObject(seed), live); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: envClient, FactoryURL: "http://factory.example.test"}
	if err := r.apply(ctx, live, plan("sid123", "v1.13.9", "factory.example.test/metal-installer/sid123:v1.13.9")); err != nil {
		t.Fatal(err)
	}

	got := &tinkv1.Hardware{}
	if err := envClient.Get(ctx, client.ObjectKeyFromObject(seed), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.UserData == nil || *got.Spec.UserData != "#cloud-config machine config" {
		t.Error("resolver apply damaged another manager's spec.userData")
	}
	if got.Spec.Metadata == nil || got.Spec.Metadata.Instance == nil || got.Spec.Metadata.Instance.OperatingSystem == nil {
		t.Fatal("operating_system was not written")
	}
	os := got.Spec.Metadata.Instance.OperatingSystem
	if os.Slug != "sid123" || os.OsSlug != "talos-v1.13.9-amd64" {
		t.Errorf("operating_system = %+v", os)
	}
	if got.Annotations[InstallerImageAnnotation] != "factory.example.test/metal-installer/sid123:v1.13.9" {
		t.Errorf("installer-image annotation = %q", got.Annotations[InstallerImageAnnotation])
	}
	assertOwns(t, ctx, seed, FieldManager, "operating_system")
	assertOwns(t, ctx, seed, "discovery", "userData")
}

// TestEnvtestCoexistenceOwnershipTransfer asserts ForceOwnership pulls operating_system away
// from the retired talos-os-metadata mirror without a conflict.
func TestEnvtestCoexistenceOwnershipTransfer(t *testing.T) {
	ns := newNamespace(t)
	ctx := context.Background()

	hw := &tinkv1.Hardware{ObjectMeta: metav1.ObjectMeta{
		Name: "hw-2", Namespace: ns,
		Labels: map[string]string{OwnerNameLabel: "m1", OwnerNamespaceLabel: ns},
	}}
	if err := envClient.Create(ctx, hw, client.FieldOwner("bootstrap")); err != nil {
		t.Fatal(err)
	}
	// Old C2 mirror owns operating_system first.
	mirror := &Reconciler{Client: envClient, FactoryURL: "http://factory.example.test"}
	live := &tinkv1.Hardware{}
	_ = envClient.Get(ctx, client.ObjectKeyFromObject(hw), live)
	applyAs(ctx, t, "talos-os-metadata", live, plan("old", "v1.13.8", "factory.example.test/metal-installer/old:v1.13.8"))

	_ = envClient.Get(ctx, client.ObjectKeyFromObject(hw), live)
	if err := mirror.apply(ctx, live, plan("sid123", "v1.13.9", "factory.example.test/metal-installer/sid123:v1.13.9")); err != nil {
		t.Fatalf("ownership transfer must not conflict: %v", err)
	}
	got := &tinkv1.Hardware{}
	_ = envClient.Get(ctx, client.ObjectKeyFromObject(hw), got)
	if got.Spec.Metadata.Instance.OperatingSystem.Slug != "sid123" {
		t.Error("operating_system value not updated after ownership transfer")
	}
	assertOwns(t, ctx, hw, FieldManager, "operating_system")
}

// TestEnvtestStaleResourceVersionConflicts asserts the optimistic precondition rejects a stale apply.
func TestEnvtestStaleResourceVersionConflicts(t *testing.T) {
	ns := newNamespace(t)
	ctx := context.Background()
	hw := &tinkv1.Hardware{ObjectMeta: metav1.ObjectMeta{
		Name: "hw-3", Namespace: ns,
		Labels: map[string]string{OwnerNameLabel: "m1", OwnerNamespaceLabel: ns},
	}}
	if err := envClient.Create(ctx, hw); err != nil {
		t.Fatal(err)
	}
	stale := &tinkv1.Hardware{}
	if err := envClient.Get(ctx, client.ObjectKeyFromObject(hw), stale); err != nil {
		t.Fatal(err)
	}
	// Bump the object so the captured resourceVersion goes stale.
	stale2 := stale.DeepCopy()
	stale2.Annotations = map[string]string{"x": "y"}
	if err := envClient.Update(ctx, stale2); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: envClient, FactoryURL: "http://factory.example.test"}
	err := r.apply(ctx, stale, plan("sid123", "v1.13.9", "factory.example.test/metal-installer/sid123:v1.13.9"))
	if !apierrors.IsConflict(err) {
		t.Fatalf("stale apply err = %v, want Conflict", err)
	}
}
```

- [ ] **Step 2: Run envtest to verify it fails**

Run: `make test-envtest` (after Step 3's Makefile edit) — or before wiring, run `CGO_ENABLED=1 go build -tags envtest ./internal/resolve/...`
Expected: FAIL — build error / `SetupWithManager` undefined, or (once compiling) the resolver package has no `setup.go`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/resolve/setup.go`:

```go
package resolve

import (
	"context"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TalosConfigGVK identifies the bootstrap config, watched as unstructured.
var TalosConfigGVK = schema.GroupVersionKind{
	Group:   "bootstrap.cluster.x-k8s.io",
	Version: "v1beta1",
	Kind:    "TalosConfig",
}

// SetupWithManager wires the controller. Primary reconcile is TinkerbellMachine (the concrete
// talosVersion is reached through it, and it fires on claim). Watches on Hardware (keyed on CAPT's
// claim labels) and on the bootstrap TalosConfig (unstructured) retrigger on annotation changes,
// re-classification, terraform re-applies, and version bumps (§3.5).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, concurrency int) error {
	primary := &unstructured.Unstructured{}
	primary.SetGroupVersionKind(TinkerbellMachineGVK)

	hardware := &tinkv1.Hardware{}

	talosConfig := &unstructured.Unstructured{}
	talosConfig.SetGroupVersionKind(TalosConfigGVK)

	return ctrl.NewControllerManagedBy(mgr).
		Named(Name).
		For(primary).
		Watches(hardware, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			return hardwareToTinkerbellMachine(obj)
		})).
		Watches(talosConfig, handler.EnqueueRequestsFromMapFunc(r.talosConfigToTinkerbellMachine)).
		WithOptions(controller.Options{MaxConcurrentReconciles: concurrency}).
		Complete(r)
}

// hardwareToTinkerbellMachine maps a Hardware to the TinkerbellMachine named by its claim labels.
func hardwareToTinkerbellMachine(obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	name := labels[OwnerNameLabel]
	namespace := labels[OwnerNamespaceLabel]
	if name == "" || namespace == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: namespace, Name: name}}}
}

// talosConfigToTinkerbellMachine maps a bootstrap TalosConfig to its Machine's TinkerbellMachine
// via the owner Machine's infrastructureRef.
func (r *Reconciler) talosConfigToTinkerbellMachine(ctx context.Context, obj client.Object) []reconcile.Request {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Kind != "Machine" {
			continue
		}
		machine := &clusterv1.Machine{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: obj.GetNamespace(), Name: ref.Name}, machine); err != nil {
			return nil
		}
		if machine.Spec.InfrastructureRef.Name == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: client.ObjectKey{
			Namespace: machine.Namespace, Name: machine.Spec.InfrastructureRef.Name,
		}}}
	}
	return nil
}
```

Create `cmd/talos-image-resolver/main.go`:

```go
// The talos-image-resolver manager resolves a per-machine Talos Image Factory schematic and
// version and writes the image identity onto claimed Tinkerbell Hardware
// (spec.metadata.instance.operating_system + a talos.tinkerbell.org/installer-image annotation),
// coexisting with CAPT's own status resolution. See docs/runtime-extensions-migration.md §3.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/tinkerbell-community/tinkerbell-bmc-discovery-controller/internal/logging"
	"github.com/tinkerbell-community/tinkerbell-bmc-discovery-controller/internal/resolve"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	builtBy = "unknown"
)

func main() {
	var (
		watchNamespace string
		factoryURL     string
		tootlesURL     string
		tinkerbellIP   string
		concurrency    int
		leaderElect    bool
		metricsAddr    string
		probeAddr      string
		logLevel       string
		logFormat      string
	)
	flag.StringVar(&watchNamespace, "watch-namespace", "", "Namespace to watch; empty watches all namespaces.")
	flag.StringVar(&factoryURL, "factory-url", "", "Image Factory base URL; empty selects the public factory.")
	flag.StringVar(&tootlesURL, "tootles-url", "", "Tootles base URL (e.g. http://10.0.0.1:7080) for the talos.config kernel arg.")
	flag.StringVar(&tinkerbellIP, "tinkerbell-ip", "", "Tinkerbell IP; used to build the tootles URL when --tootles-url is unset.")
	flag.IntVar(&concurrency, "concurrency", 4, "Maximum concurrent reconciles.")
	flag.BoolVar(&leaderElect, "leader-elect", true, "Enable leader election.")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Metrics endpoint bind address.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Health probe bind address.")
	flag.StringVar(&logLevel, "log-level", "info", "Log level: debug, info, warn, or error.")
	flag.StringVar(&logFormat, "log-format", "json", "Log format: json or text.")
	flag.Parse()

	root, err := logging.New(logLevel, logFormat, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctrl.SetLogger(logr.FromSlogHandler(root.Handler()))
	log := logging.Component(root, "setup")
	log.Info(resolve.Name, "version", version, "commit", commit, "date", date, "builtBy", builtBy)

	tootlesUserData, err := resolve.TootlesUserDataURL(tootlesURL, tinkerbellIP)
	if err != nil {
		log.Error("invalid tootles configuration", "err", err)
		os.Exit(1)
	}

	factory := factoryURL
	if factory == "" {
		factory = resolve.DefaultFactoryURL
	}

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, clusterv1.AddToScheme, tinkv1.AddToScheme} {
		if err := add(scheme); err != nil {
			log.Error("unable to build scheme", "err", err)
			os.Exit(1)
		}
	}

	cacheOptions := cache.Options{}
	if watchNamespace != "" {
		cacheOptions.DefaultNamespaces = map[string]cache.Config{watchNamespace: {}}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "talos-image-resolver.resolve.tinkerbell.org",
		Cache:                  cacheOptions,
	})
	if err != nil {
		log.Error("unable to create manager", "err", err)
		os.Exit(1)
	}

	reconciler := &resolve.Reconciler{
		Client:        mgr.GetClient(),
		Recorder:      mgr.GetEventRecorderFor(resolve.Name),
		Registrar:     resolve.NewRegistrar(factoryURL),
		Policy:        resolve.VersionPolicy{Versions: resolve.NewVersionResolver(factoryURL)},
		Customization: resolve.CustomizationConfig{TootlesUserDataURL: tootlesUserData},
		FactoryURL:    factory,
	}
	if err := reconciler.SetupWithManager(mgr, concurrency); err != nil {
		log.Error("unable to set up controller", "err", err)
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error("unable to set up health check", "err", err)
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error("unable to set up ready check", "err", err)
		os.Exit(1)
	}

	log.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error("manager exited with error", "err", err)
		os.Exit(1)
	}
}
```

> Note: `mgr.GetEventRecorderFor` returns the client-go `events.EventRecorder` used by `Reconciler.Recorder`; if the repo's manager surface uses `GetEventRecorder(name)` (as janitor does), match that call exactly.

Create the helm chart. `helm/talos-image-resolver/Chart.yaml`:

```yaml
apiVersion: v2
name: talos-image-resolver
description: Resolves per-machine Talos Image Factory schematics and writes image identity onto claimed Tinkerbell Hardware.
type: application
version: 0.1.0
appVersion: "0.1.0"
```

`helm/talos-image-resolver/values.yaml`:

```yaml
image:
  repository: ghcr.io/tinkerbell-community/tinkerbell-bmc-discovery-controller
  tag: ""
  pullPolicy: IfNotPresent
resolver:
  watchNamespace: ""
  factoryURL: ""
  tootlesURL: ""
  tinkerbellIP: ""
  concurrency: 4
leaderElection: true
logging:
  level: info
  format: json
resources: {}
```

`helm/talos-image-resolver/templates/_helpers.tpl`:

```yaml
{{- define "resolver.name" -}}talos-image-resolver{{- end -}}
{{- define "resolver.labels" -}}
app.kubernetes.io/name: {{ include "resolver.name" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}
{{- define "resolver.selectorLabels" -}}
app.kubernetes.io/name: {{ include "resolver.name" . }}
{{- end -}}
```

`helm/talos-image-resolver/templates/serviceaccount.yaml`:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "resolver.name" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "resolver.labels" . | nindent 4 }}
```

`helm/talos-image-resolver/templates/rbac.yaml` — least-privilege union; the `get talosconfigs` grant is load-bearing (§3.4):

```yaml
{{- $scoped := ne .Values.resolver.watchNamespace "" }}
apiVersion: rbac.authorization.k8s.io/v1
kind: {{ ternary "Role" "ClusterRole" $scoped }}
metadata:
  name: {{ include "resolver.name" . }}
  {{- if $scoped }}
  namespace: {{ .Values.resolver.watchNamespace }}
  {{- end }}
  labels:
    {{- include "resolver.labels" . | nindent 4 }}
rules:
  - apiGroups: ["infrastructure.cluster.x-k8s.io"]
    resources: ["tinkerbellmachines"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["cluster.x-k8s.io"]
    resources: ["machines"]
    verbs: ["get", "list", "watch"]
  # Load-bearing: a Forbidden here silently disables resolution (spec §3.4, architecture.md P3).
  - apiGroups: ["bootstrap.cluster.x-k8s.io"]
    resources: ["talosconfigs"]
    verbs: ["get", "list", "watch"]
  # Sparse server-side apply of operating_system uses the patch verb.
  - apiGroups: ["tinkerbell.org"]
    resources: ["hardware"]
    verbs: ["get", "list", "watch", "patch"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]
  - apiGroups: ["events.k8s.io"]
    resources: ["events"]
    verbs: ["create", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: {{ ternary "RoleBinding" "ClusterRoleBinding" $scoped }}
metadata:
  name: {{ include "resolver.name" . }}
  {{- if $scoped }}
  namespace: {{ .Values.resolver.watchNamespace }}
  {{- end }}
  labels:
    {{- include "resolver.labels" . | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: {{ ternary "Role" "ClusterRole" $scoped }}
  name: {{ include "resolver.name" . }}
subjects:
  - kind: ServiceAccount
    name: {{ include "resolver.name" . }}
    namespace: {{ .Release.Namespace }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ include "resolver.name" . }}-local
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "resolver.labels" . | nindent 4 }}
rules:
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "create", "update"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ include "resolver.name" . }}-local
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "resolver.labels" . | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ include "resolver.name" . }}-local
subjects:
  - kind: ServiceAccount
    name: {{ include "resolver.name" . }}
    namespace: {{ .Release.Namespace }}
```

`helm/talos-image-resolver/templates/deployment.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "resolver.name" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "resolver.labels" . | nindent 4 }}
spec:
  replicas: 1
  selector:
    matchLabels:
      {{- include "resolver.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "resolver.selectorLabels" . | nindent 8 }}
    spec:
      serviceAccountName: {{ include "resolver.name" . }}
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: manager
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          command: ["/talos-image-resolver"]
          args:
            - --watch-namespace={{ .Values.resolver.watchNamespace }}
            - --factory-url={{ .Values.resolver.factoryURL }}
            - --tootles-url={{ .Values.resolver.tootlesURL }}
            - --tinkerbell-ip={{ .Values.resolver.tinkerbellIP }}
            - --concurrency={{ .Values.resolver.concurrency }}
            - --leader-elect={{ .Values.leaderElection }}
            - --log-level={{ .Values.logging.level }}
            - --log-format={{ .Values.logging.format }}
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
            readOnlyRootFilesystem: true
          ports:
            - name: metrics
              containerPort: 8080
            - name: probes
              containerPort: 8081
          livenessProbe:
            httpGet:
              path: /healthz
              port: probes
          readinessProbe:
            httpGet:
              path: /readyz
              port: probes
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
```

Modify `Makefile` — extend the `test-envtest` target's package list:

```makefile
	KUBEBUILDER_ASSETS="$$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.24 use $(ENVTEST_K8S_VERSION) -p path)" \
		CGO_ENABLED=1 go test -race -tags envtest -run 'TestEnvtest' ./internal/sync/... ./internal/janitor/... ./internal/resolve/...
```

> Envtest CRD note: the Hardware CRD already lives in `test/crds` (used by the janitor/sync envtests). The resolver envtest exercises only the Hardware SSA path (it calls `r.apply` directly, seeding a Hardware), so no TinkerbellMachine/TalosConfig/Machine CRDs are needed. If `test/crds` does not already contain the Hardware CRD, copy it there as part of this step.

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=1 go test -race ./internal/resolve/...` (unit) and `make test-envtest` (managed-fields).
Expected: PASS. Also run `helm lint helm/talos-image-resolver` and `CGO_ENABLED=0 go build ./cmd/talos-image-resolver`.
Expected: chart lints clean; binary builds.

- [ ] **Step 5: Lint + commit**

Run: `make lint && make fmt-check`
Expected: no findings.

```bash
git add internal/resolve/setup.go internal/resolve/envtest_test.go cmd/talos-image-resolver helm/talos-image-resolver Makefile
git commit -m "feat(resolve): controller wiring, cmd, helm chart + RBAC (get talosconfigs), envtest SSA ownership"
```

---

## Self-Review

**Spec coverage (§3.2–§3.5 → task):**

| Spec requirement | Task |
| --- | --- |
| §3.1 engine moves to `internal/resolve` (schematic build/registration/version resolution) | Tasks 1–3 |
| §3.1 version policy moves to `version_policy.go` | Task 5 |
| §3.1 kernel args / overlay / bootloader folded into `Customization` | Tasks 1 (types) + 4 (assembly) |
| §3.1 writing identity onto `Hardware.operating_system` (dynamic SSA) | Task 6 (`apply`) + Task 7 (envtest) |
| §3.2 `Customization` grows `ExtraKernelArgs`/`Overlay`/`Bootloader`; one ID backs both artifacts | Task 1 (`Customization`, `Build` pass-through) |
| §3.2 `talos.config=http
