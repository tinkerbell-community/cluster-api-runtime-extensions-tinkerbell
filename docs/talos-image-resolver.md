# talos-image-resolver

The provisioning-image rendezvous of [runtime-extensions-migration.md](runtime-extensions-migration.md)
§3, implemented in `internal/resolve` and deployed by `helm/talos-image-resolver`. It resolves a
per-machine Talos Image Factory schematic and version and writes the image identity onto claimed
Tinkerbell Hardware — replacing terraform's static bake-in and CAPT's `status` trio — and it serves
the **mandatory Workflow-CREATE admission gate** that makes that delivery safe. This document is the
operational reference; the migration document is the binding design.

## What it writes

Sole writer of `Hardware.spec.metadata.instance.operating_system` for **claimed** Hardware (owner
labels present), field manager `talos-image-resolver`, sparse server-side apply with the read
`resourceVersion` as an optimistic precondition:

| Field | Value | Consumed by |
| --- | --- | --- |
| `slug` | registered, content-addressed schematic ID | Template `IMG_URL` |
| `version` | full resolved version, e.g. `v1.13.9` | Template `IMG_URL` |
| `os_slug` | `talos-<version>-<arch>` (dash form, never underscores) | Template arch extraction |
| `image_tag`, `distro` | `<version>`, `talos` | tootles EC2 metadata |

Plus two resolver-owned annotations: `talos.tinkerbell.org/installer-image` (the upgrade-path
rendezvous read by CABPT under Strategy B) and `talos.tinkerbell.org/contract` (the pinned minor for
machines with no explicit version).

It never asserts `spec.userData` (CAPT), `spec.disks`/`instance.id`/`instance.hostname`
(discovery/terraform), or the C1 annotations it reads. Provisioned Hardware
(`v1alpha1.tinkerbell.org/provisioned`) is frozen — `operating_system` is never recomputed — while
the `installer-image` annotation keeps refreshing so in-place upgrades stay current.

## The Workflow-CREATE gate

Vanilla CAPT creates the Workflow at claim with no `operating_system` awareness, the resolver writes
the block asynchronously after claim, and tink-controller renders a Workflow **exactly once**. A
render against an empty block builds `IMG_URL = https://factory.talos.dev/image///metal-.raw.zst`
and permanently bricks the machine (deleting a Workflow to re-render is forbidden). The gate is the
sole ordering mechanism:

- ValidatingWebhookConfiguration on `workflows.tinkerbell.org` **CREATE**, `failurePolicy: Fail`,
  default-on.
- `objectSelector` requires CAPT's `capt.tinkerbell.org/machine-name` label to exist, so tink
  auto-enrollment Workflows (unlabeled) are excluded at the API server before the webhook is called.
- Denies until the target Hardware's `operating_system` carries `slug`, `version`, and `os_slug`;
  CAPT backoff-retries the create, so a deny is retriable pressure, not a failure.
- Served by every replica (webhook serving is not leader-gated); reads through the uncached API
  reader so admission never depends on informer sync.

`--enable-workflow-gate=false` exists for bring-up ordering only. It is never a steady-state
posture: with the gate off, any unseeded Hardware (all discovery-created Hardware, and any Hardware
the janitor cleared on release) can be claimed and bricked.

## The shipped Template

The chart ships the `talos-install` Workflow Template CR (`files/talos-install-template.yaml`,
byte-identical to the terraform body it replaces) with a **content-addressed name**:
`talos-install-<sha256(body) | trunc 10>`. A body change ships a *new* Template object — an
in-place mutation would silently affect only future machines and drift from provisioned ones.
Wave 1 ships it additively: nothing references it until the Wave 2 ClusterClass
`TinkerbellMachineTemplate`s name it via `templateRef`, and the live control-plane templates keep
their `templateInline` untouched (CAPT's immutability webhook refuses the swap on a live template,
and a template rotation would roll the control plane).

## Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--factory-url` | public Factory | Image Factory base URL |
| `--tootles-url` / `--tinkerbell-ip` | — | **required** (one of them): builds the `talos.config` kernel arg; fail-fast at startup |
| `--enable-workflow-gate` | `true` | serve the admission gate |
| `--webhook-port` / `--webhook-cert-dir` | `9443` / controller-runtime default | webhook serving |
| `--watch-namespace` | all | scope cache + reconciles |

## Terraform → resolver handover (P4) runbook

The ordered rollout from migration §3.7; each step is independently reversible.

1. **Deploy the resolver** (this chart, gate default-on). Coexistence is safe: terraform-seeded
   fleet Hardware keeps its static block (which keeps the inline Template renderable pre-claim);
   claimed Hardware gets resolver values, taking SSA ownership from any prior writer benignly.
2. **Canary-verify** on one unprovisioned machine: confirm the resolver-written block yields a
   Factory-valid bootable image — `talos.config=` present in the schematic, node leaves maintenance
   mode and provisions. Do **not** gate on cross-Hardware value equality: per-machine resolver
   schematics legitimately differ from terraform's per-arch base schematics.
3. **Apply the terraform P4 change** (`cluster-bootstrap` modules): `manage_operating_system` is
   `false` for every fleet node — terraform seeds `operating_system` at create and then treats it as
   server-owned (`computed_fields`) — and `true` only for the bootstrap node (index 0, outside CAPI,
   never claimed), which terraform keeps authoring. After this apply the resolver is the sole writer
   of the claimed fleet.
4. **Rollback**: scale the resolver to 0 → CAPT still resolves its own status (until SP-9) and
   terraform re-owns metadata by reverting the module change; expect write churn, not corruption —
   provisioned-freeze means no running node is ever re-imaged.

Until step 3 lands, a `terraform apply` stomps resolver-written blocks and the resolver forces them
back on its next reconcile — churn, not corruption.

## Metrics and events

`resolver_identities_resolved_total`, `resolver_versions_unresolved_total`,
`resolver_apply_conflicts_total`; events on the TinkerbellMachine for unresolved versions and
Factory failures. Steady state makes no Factory calls (content-hash registration cache, 10-minute
`/versions` TTL with stale-on-error fallback).
