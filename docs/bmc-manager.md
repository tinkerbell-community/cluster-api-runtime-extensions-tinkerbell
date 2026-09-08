# bmc-manager — Intel AMT/vPro BMC management

`cmd/bmc-manager` manages Intel AMT devices (Intel NUCs and similar vPro
platforms) as first-class BMCs: it discovers them, collects inventory, holds
their credentials, registers them as Tinkerbell `Machine` + `Hardware`, and
exposes the fleet through a DMTF Redfish aggregator.

Design date: 2026-09-07. Status: implemented on branch `bmc-manager`.

This document is the decision record. Every claim marked **measured** was
verified against a live device (ASUS NUC15CRHV7, AMT 18.1.18) during design;
the probe programs are throwaway and not part of this repo.

---

## Why this exists

`cmd/bmc-discovery` finds BMCs over mDNS and talks to them over Redfish. Intel
AMT devices do neither: they do not advertise mDNS or SSDP, and they speak
WS-Management, not Redfish. They are invisible to the existing controller.

bmclib has an `intelamt` provider, but it is far narrower than it looks.

### Measured: what bmclib's AMT provider implements

Verified by interface assertion and live calls against 10.0.0.160:

| Capability | Present |
|---|---|
| `PowerSet` (on/off/cycle) | yes |
| `PowerStateGet` | yes |
| `BootDeviceSet` | **`pxe` only** — all other devices rejected client-side |
| `Inventory`, `SetVirtualMedia`, `BootDeviceOverrideGet`, `BmcReset`, `Screenshot`, `SendNMI`, `Get/SetBiosConfiguration`, `FirmwareInstall`, `UserRead`, `GetSystemEventLog`, `PostCode` | **no** |

Three further defects:

1. **No TLS verification.** `jacobweinstock/iamt` connects to AMT's self-signed
   certificate without validating it and exposes no way to pin or verify.
2. **Defaults are wrong for TLS devices** — port 16992/`http`, while a
   TLS-enabled device has 16992 closed.
3. `BootDeviceSet` ignores its own `setPersistent` and `efiBoot` arguments, and
   there is no way to clear a boot override.

**Decision D1: `pkg/amt` is built on `github.com/device-management-toolkit/go-wsman-messages/v2`, not bmclib.**
bmclib remains in the picture only because rufio already speaks it for power and
PXE, which is the one thing it does adequately.

---

## Decisions

| # | Decision | Rationale |
|---|---|---|
| D1 | `pkg/amt` on go-wsman-messages | bmclib covers ~3 of 15 capabilities; no inventory, no media, no certs |
| D2 | Redfish **aggregator**, one service root | Spec-faithful (`AggregationService`/`AggregationSource`); control path stays native |
| D3 | New CRDs `AMTDevice` + `AMTProfile`, group `amt.tinkerbell.org` | Tinkerbell `Machine`/`Hardware` keep their existing field owners; C0 SSA discipline unaffected |
| D4 | `AMTProfile` is **cluster-scoped** | Fleet-wide policy |
| D5 | `VirtualMedia` via **OCR UEFI HTTPS**, never IDER | See "Virtual media" below — measured |
| D6 | `VirtualMedia` advertised **conditionally** on `status.bootCapabilities.uefiHTTPS` | An honest 404 beats a stub that fails at boot |
| D7 | Boot image server folded into `cmd/bmc-manager` | AMT must trust the image server's cert; co-locating keeps cert and root in sync |
| D8 | **No `amt-activate` action in v1**; onboard with `rpc-go` by hand | See "Onboarding" — the activation subsystem is deferred, not designed away |
| D9 | Aggregator reads served from `AMTDevice.status`; writes go straight to WS-Man | Reads at request cadence would hammer the fleet; writes must not be eventually-consistent |
| D10 | Pin AMT TLS cert fingerprint in `status` | Closes the bmclib verification hole for a few lines |
| D11 | No SSDP in v1 | Optional per DSP0266 §12.4; consumers are in-cluster |
| D12 | Lives in this repo as a third deployable, not a separate repo | Superseded the original separate-repo decision; see note below |

**Note on D12.** The design originally chose a standalone repository. That was
superseded by the instruction to implement `bmc-manager` here. The package
boundaries (`pkg/amt` depends on nothing in this repo) are kept clean enough
that extraction later is mechanical.

---

## Onboarding (D8)

AMT is **not** ACM-by-default. Measured on the reference device, whose own audit
log records the transition:

```
2026-09-06T17:43:10 | AMT Provisioning Started   | init=Local
    "Intel AMT transitioned to setup mode."
2026-09-06T17:43:10 | AMT Provisioning Completed | init=Local
    "Provisioning Method: Host-Based Provisioning Admin Mode"
```

A factory device sits in `ProvisioningState: PreProvisioning` with
`CurrentControlMode: NotProvisioned` and no admin password, and will not answer
authenticated WS-Man. Something must provision each unit.

v1 does not automate that. Each NUC is onboarded once, by hand, with `rpc-go`
from a live USB or the pre-wipe OS. The controller then:

- **surfaces** unprovisioned devices as an `AMTDevice` condition rather than
  ignoring them, so a newly racked NUC is visible in `kubectl get amtdevices`;
- **discovers credentials** by pivoting an ordered list of candidate Secrets and
  verifying each with a real WS-Man session — the same pattern
  `cmd/bmc-discovery` already uses;
- **can rotate** the admin password over the network via
  `AMT_AuthorizationService.SetAdminAclEntryEx(username, digestPassword)`, where
  `digestPassword = MD5(user:realm:pass)` and `realm` comes from
  `AMT_GeneralSettings.DigestRealm`.

**Rotation is a capability, not yet a behaviour.** `pkg/amt.SetAdminPassword` is
implemented and unit-tested, and `status.digestRealm` is recorded so it can be
called, but the reconciler does not invoke it: `AMTProfile.password.rotation`
and `status.lastRotatedTime` are inert in this release. The plumbing that makes
rotation *safe* is in place, which is the part worth getting right first.

That plumbing matters because rotation has an unavoidable window: once the write
lands the old password is dead, so a failed verification afterwards is
ambiguous. The per-device Secret carries **`password` and `previousPassword`**,
and the credential pivot tries current then previous, so a half-failed rotation
converges on the next reconcile instead of locking the controller out. Enable
rotation only after exercising it on a spare device.

**Remaining physical-access case:** a device whose password is in neither the
Secret nor any candidate list.

### Deferred: the in-band activation path

Not built, but the seam is preserved. An `amt-activate` HookOS action running in
the ramdisk would talk to AMT locally over `/dev/mei0`, which does **not**
require the network password — making unprovision-and-reactivate a remote
recovery for any device in an unknown state. Two experiments gate it:

- **E1** — on a factory-fresh NUC, read `IPS_HostBasedSetupService.AllowedControlModes`.
  If it contains `Admin`, cert-free host-based ACM is possible fleet-wide. The
  reference device reports `[Admin, Client]`, but it had also been through MEBx,
  so this is unresolved.
- **E2** — confirm HookOS ships `mei_me` and exposes `/dev/mei0`.

---

## Virtual media (D5)

Two paths exist. Both were tested against the live device.

### Path A — OCR UEFI HTTPS (chosen)

AMT fetches the boot image itself from an HTTPS URL. No session to hold.

**Measured:** `AMT_BootCapabilities.ForceUEFIHTTPSBoot: true` and
`AMT_BootSettingData.UEFIHTTPSBootEnabled: true`. Configuring it —
`OCR_EFI_NETWORK_DEVICE_PATH` TLV → `UefiBootParametersArray` →
`ChangeBootOrder(OCRUEFIHTTPS)` → `SetBootConfigRole` — returned `rc=0` at every
step and the machine reset into the attempt. The device's audit log recorded
`Special Command: Intel Command - HTTPS Boot`.

Constraints inherited from the hardware:

- the URL must be **HTTPS with a certificate AMT trusts** — hence D7, and the
  root is pushed via `AMT_PublicKeyManagementService.AddTrustedRootCertificate`;
- `EnforceSecureBoot` interacts with whether the image is signed; defaulted false
  and exposed in `AMTProfile` rather than chosen silently.

### Path B — IDER (rejected)

**Measured, and it gets further than expected:** TLS to 16995 connects,
`START_REDIRECTION_SESSION("IDER")` returns `status=0x00 success`, auth
negotiation returns digest-only, and the digest challenge parses cleanly —
`realm="Digest:878D65F7953A0B2AC7683C7AFA58D3F3"`, byte-identical to
`AMT_GeneralSettings.DigestRealm`.

It fails at the digest **response**: seven variants (POST/GET × four URIs ×
RFC-2617 qop and RFC-2069) were all rejected with an immediate connection close.
A uniform EOF across algorithmic *and* structural variants means the message
shape is wrong, and that layout is undocumented.

Solving it would not be enough:

1. **Wrong protocol generation.** Per Intel's developer guide, storage
   redirection uses **USB-R from AMT 11.0 onward**, not IDE-R. The device is AMT
   18. `UseIDER`/`IDERBootDevice`/`BootCapabilities.IDER` are a compatibility
   façade. MeshCentral's `amt-ider.js` — the only reference implementation —
   targets the protocol this hardware does not use.
2. **The data plane is a device emulator**: INQUIRY, TEST UNIT READY, READ
   CAPACITY, READ TOC, MODE SENSE, GET CONFIGURATION, READ(10), streaming
   2048-byte sectors for the whole boot.
3. **It fights the controller model** — a long-lived stateful socket per booting
   machine, held inside a reconciler.

Path A was ~30 lines and worked first try.

---

## Other measured facts that shaped the design

- **Boot settings are one-shot.** After a forced boot is consumed, firmware
  clears `UefiBootNumberOfParams` back to 0. Boot override is therefore a
  fire-and-verify action, not desired state to converge on. This maps exactly
  onto Redfish `BootSourceOverrideEnabled: Once`; `Continuous` is rejected as
  unsupported.
- **The redirection listener is separate from the redirection state.**
  `AMT_RedirectionService.EnabledState: 32771` (IDER+SOL enabled) with
  `ListenerEnabled: false` leaves 16994/16995 **closed**. Setting
  `ListenerEnabled: true` opened 16995 (16994 stays closed under TLS). bmclib
  cannot do this at all, so SOL is unreachable through it.
- **Inventory over CIM is complete enough for Redfish.** `CIM_Chassis`
  (model/serial), `CIM_Card` (baseboard), `CIM_Processor`, `CIM_PhysicalMemory`,
  `CIM_BIOSElement`, `CIM_SoftwareIdentity`, `CIM_EthernetPort` and
  `CIM_ComputerSystemPackage` (`PlatformGUID`) all returned populated data.
- **`PlatformGUID` is the stable identity** — survives reboots, IP changes and
  renames. It keys every Redfish resource.

---

## Architecture

```
                     ┌──────────────── cmd/bmc-manager ────────────────┐
                     │                                                 │
  AMT device         │  ┌──────────────┐      ┌───────────────────┐     │
  (WS-Man 16993) ◄───┼──┤ enroll       │      │ redfish aggregator│     │
                     │  │ reconciler   │─────►│  + image server   │◄────┼── clients
                     │  └──────┬───────┘      └───────────────────┘     │
                     │         │ pkg/amt                                │
                     └─────────┼────────────────────────────────────────┘
                               ▼
              Machine{providerOptions.intelAMT} + Hardware
                               ▼
                  rufio → bmclib IntelAMT → power/PXE
```

The **control path for power and PXE does not traverse the aggregator** — rufio
talks AMT natively and already works. The aggregator is a north-facing API
surface; if it is down, provisioning continues.

| Package | Responsibility |
|---|---|
| `pkg/amt` | The only code that knows WS-Man exists. Session, facts, inventory→`common.Device`, power, boot, media, certs. |
| `api/amt/v1alpha1` | `AMTDevice`, `AMTProfile` |
| `internal/amtenroll` | Reconciler: discover → verify credentials → inventory → register |
| `internal/redfish` | Aggregator + boot image server |
| `cmd/bmc-manager` | Wiring |

## Redfish mapping

| Redfish | AMT source |
|---|---|
| `Systems/{guid}` | `CIM_ComputerSystem` + `Chassis` + `Processor` + `PhysicalMemory` |
| `PowerState` | `CIM_AssociatedPowerManagementService.PowerState` (2=On, 8=Off) |
| `ComputerSystem.Reset` | `RequestPowerStateChange`: On→2, ForceOff→8, ForceRestart→10, PowerCycle→5, GracefulShutdown→12, Nmi→11 |
| `Boot.BootSourceOverrideTarget` | `ChangeBootOrder` + `SetBootConfigRole(1)` |
| `BootSourceOverrideEnabled: Once` | `SetBootConfigRole(1)` |
| `BootSourceOverrideEnabled: Disabled` | `SetBootConfigRole(32768)` |
| `VirtualMedia.InsertMedia` | OCR UEFI HTTPS TLV |
| `Chassis/{guid}` | `CIM_Chassis` + `CIM_Card` |
| `Managers/{guid}` | `AMT_GeneralSettings` + `CIM_SoftwareIdentity` |
| `AggregationService/AggregationSources/{guid}` | one per `AMTDevice` |

## Trust model

| Leg | Design |
|---|---|
| controller → AMT | pin `status.tlsFingerprint` on first verified connect; refuse on mismatch (D10) |
| controller → AMT (future) | replace AMT's self-signed cert via `GeneratePKCS10RequestEx` + `AddCertificate` + `TLSSettingData` |
| client → aggregator | Redfish `SessionService` + Basic over TLS |
| AMT → image server | AMT verifies the image server against the root pushed to it |

## What is built

| Component | State |
|---|---|
| `pkg/amt` | Facts, CIM inventory to `common.Device`, power, one-shot boot override, UEFI HTTPS virtual media, password set, certificate pinning. Validated against live hardware. |
| `api/amt/v1alpha1` | `AMTDevice`, `AMTProfile`, generated CRDs |
| `internal/amtenroll` | Reconciler: profile resolution, credential pivot, facts and inventory into status, Machine + Hardware registration under its own SSA field manager |
| `internal/redfish` | Aggregator (ServiceRoot, Systems, Chassis, Managers, VirtualMedia, AggregationService) and the boot image server |
| `cmd/bmc-manager` | Manager binary, goreleaser build and image |
| `charts/bmc-manager` | Chart, split RBAC, default `AMTProfile`, wired into `make components` |

Tests: unit coverage for every package, five envtest cases against a real API
server for the SSA guarantees, and read-only integration tests against a live
device gated on `AMT_HOST`.

## Deliberately not built

- **Activation** (D8). Devices are onboarded out of band with `rpc-go`.
- **Password rotation**, per above.
- **Reconciling `AMTProfile` onto devices.** The profile's redirection, consent
  and time-sync policy is recorded and served but not yet applied to hardware;
  `IPS_OptInService.OptInRequired` can be set to 0 in ACM for consent-free KVM,
  and is exposed in the CRD for that purpose.
- **SSDP** (D11).

## Open items

- **E1/E2** above, gating the deferred activation path.
- `make components` fails on the pre-existing
  `cluster-api-runtime-extensions-tinkerbell` chart, which requires
  `resolver.tootlesURL` or `resolver.tinkerbellIP`. This predates bmc-manager;
  the bmc-manager chart renders cleanly on its own.
- The Redfish service UUID has no default. It is omitted when unset rather than
  emitted blank, but a stable value should be configured so clients see a
  consistent service identity across restarts.
