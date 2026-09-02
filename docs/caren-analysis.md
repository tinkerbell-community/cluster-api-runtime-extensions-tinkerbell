# CAREN Analysis: Multi-Provider Runtime Extensions Done Right

Status: Reference — 2026-09-01 (analyzed at commit `60b8837`, shallow clone of `main`)

[CAREN](https://github.com/nutanix-cloud-native/cluster-api-runtime-extensions-nutanix)
(cluster-api-runtime-extensions-nutanix) is the canonical mature CAPI Runtime SDK extension repo:
one binary serving five provider families (AWS, Docker, Nutanix, EKS, generic) through 33 handlers
under a single `ExtensionConfig`, plus admission webhooks and plain controllers in the same process.
This document records how it supports multiple provider types, how it references CRDs, and how its
CAPI integration is wired — and distills what this repo should copy, adapt, or deliberately not do,
both today (no ClusterClass) and for the ClusterClass future sketched in
[architecture.md](architecture.md). Every claim below was verified against CAREN source with
file:line evidence by four parallel reviewers; paths are relative to the CAREN repo root.

## 1. How multiple provider types are supported

### One binary, one ExtensionConfig, 33 handlers

`cmd/main.go` instantiates six `Handlers` factories — lifecycle plus five provider families — and
concatenates their handler lists into one registration call (`cmd/main.go:101-121,173-185`).
Registration is duck-typed: `server.AddHandlers` type-asserts each handler against per-hook
interfaces (`BeforeClusterCreate`, `DiscoverVariables`, `GeneratePatches`, …) and registers one
`ExtensionHandler` per implemented hook, named `strings.ToLower(h.Name()) + suffix`
(`-dv`, `-gp`, `-bcc`, `-acpi`, `-bcu`, `-acpu`, `-bcd`, `-vt`) (`common/pkg/server/server.go:61-150`).
Each infra provider contributes six handlers (cluster + worker variables, cluster + worker patch
handlers in both current and previous generations); lifecycle addons contribute four aggregate
handlers, all named `caren`.

### Provider selection is by reference, not sniffing

The mutation path never asks "which provider is this cluster":

- **Layer 1 (primary)**: each ClusterClass references only its own provider's external patch
  handlers (`generatePatchesExtension: nutanixclusterv7configpatch-gp.<extensionconfig-name>`), so
  CAPI's topology controller simply never calls AWS handlers for a Docker cluster
  (`charts/.../defaultclusterclasses/nutanix-cluster-class.yaml:102-110`).
- **Layer 2 (defensive)**: every provider publishes the *same* variable names (`clusterConfig`,
  `workerConfig`) with provider-specific schemas nesting provider fields under
  `clusterConfig.aws` / `.nutanix` / etc.; each small mutator reads its path with `variables.Get`
  and no-ops on `IsNotFoundError`, and only mutates objects matching a CAPI `PatchSelector` against
  the request item's holder reference. An AWS-only patch living inside the *generic* mutator list
  no-ops everywhere else purely via its `AWSClusterTemplate` selector.

Only the lifecycle (addon) layer sniffs providers — and its weakest code is exactly there: the CCM
handler dispatches by *substring match* on `InfrastructureRef.Kind` (`ccm/handler.go:157-160`).
Explicit contracts beat kind-name matching.

### The meta-mutator pipeline

`NewMetaGeneratePatchesHandler` wraps an **ordered** `[]MetaMutator` and walks every templated
object with CAPI's `topologymutation.WalkTemplates`
(`common/pkg/capi/clustertopology/handlers/mutation/meta.go:44-134`). One external patch entry in a
ClusterClass buys dozens of small, individually-testable mutators with deterministic in-code
ordering (ordering is load-bearing: containerd-restart last, MetalLB after CNI/CCM) — versus N
extension handlers whose cross-extension ordering CAPI does not guarantee.

### EKS: the "not kubeadm" template

CAREN pre-split its generic mutators into `kubeadm/` and bootstrap-agnostic `generic/` subtrees.
The EKS provider (no `KubeadmControlPlane`) composes only the `generic/` subset (users,
kubeproxymode, ntp, taints) plus five-line wrappers around AWS mutator constructors
re-parameterized with a different variable path and managed-control-plane selectors
(`pkg/handlers/eks/mutation/handlers.go:14-27`). **This is the exact template for a future Talos
flavor**: reuse bootstrap-agnostic mutators, skip the kubeadm list entirely, add
`TalosControlPlaneTemplate`/`TalosConfigTemplate` selectors to shared mutators — parameterize
constructors, never fork mutator logic. Note the shipped `generic` handler pair is kubeadm-shaped,
so a Talos ClusterClass would need a new flavor, not `generic`.

### Handler-name versioning: the skew mechanism

Handler names are the public API — ClusterClasses pin them. When a mutator's *output* changes for
unchanged inputs, CAREN freezes the old behavior in a `pkg/handlers/v6` tree (forking only the one
changed package, re-importing everything else) and registers **both generations side by side**
(`nutanixClusterv6configpatch` and `...v7configpatch`), so existing ClusterClasses keep
byte-identical patch output — no surprise node rollouts — while new ClusterClasses opt into v7
(`pkg/handlers/v6/nutanix/mutation/metapatch_handler.go:32-48`). `DiscoverVariables` names stay
unversioned. For this ecosystem the stakes are higher than rollouts: patch-output drift folds into
CABPT's in-place config hash and would trigger upgrades.

## 2. How CRDs are referenced

### Schema-only CRDs: the headline pattern

CAREN's `api/v1alpha1` types (`NutanixClusterConfig`, `AWSWorkerNodeConfig`, …) are
kubebuilder-marked CRD roots whose generated YAML is **never installed on any cluster**. Instead,
`controller-gen` renders CRD YAML into `api/v1alpha1/crds/`, the YAML is `go:embed`-ded, and at
process init `variables.MustSchemaFromCRDYAML` converts
`.spec.versions[0].schema.openAPIV3Schema.properties.spec` into a ClusterClass variable JSON schema
(`common/pkg/capi/clustertopology/variables/fromcrdyaml.go:19-206`). Defaults, enums, patterns, and
CEL `XValidations` (including `self == oldSelf` immutability) authored as Go markers are then
enforced by core CAPI's topology machinery — **zero defaulting/validation webhook code for
variables, zero CRD install/upgrade burden**.

Two gotchas: ClusterClass variables support no `anyOf`, so `resource.Quantity`-shaped fields need
post-generation `yq` surgery to `type: string` (`make/go.mk:251-275`) — Talos configs are full of
these; and `minimum`/`maximum` must be whole numbers.

### One meta-variable per axis

Each provider ships exactly one required `clusterConfig` variable and one optional `workerConfig`
variable — not dozens of small ones. Per-MachineDeployment differences ride CAPI's native MD-level
variable overrides (which is why `workerConfig` is optional). An internal union type
(`api/variables.ClusterConfigSpec`, embedding every provider's blocks, "not meant to be used as a
CRD") lets webhooks and controllers unmarshal the variable once without type switches.

### Typed mutation over unstructured, diffed back

`patches.MutateIfApplicable` (`common/pkg/capi/clustertopology/patches/generator.go:24-130`) is the
core mechanic: walk request items as `*unstructured.Unstructured`; gate on a `PatchSelector` match;
convert to a typed template (`KubeadmControlPlaneTemplate`, `AWSMachineTemplate`, …) with
validation; run a typed mutation on a deep copy; **RFC6902-diff the before/after serializations and
re-apply the minimal patch to the original unstructured document**. The comment explains why: a
naive typed round-trip emits spurious changes from `omitempty`/zero-value semantics on minimal
template documents — the same class of bug this ecosystem knows from strategic-merge patch handling.

The holder-reference matcher is a re-implementation of CAPI's *private* inline-patch matcher
(`patches/matchers/match.go`), tolerating both contract field paths
(`spec.machineTemplate.infrastructureRef` v1beta1 vs `spec.machineTemplate.spec.infrastructureRef`
v1beta2) — anyone writing `GeneratePatches` handlers must copy this dual-contract tolerance.

### Dependency hygiene: vendor, never import

CAREN never imports a third-party provider Go module. `make apis.sync` rsyncs CAPA/CAPX/CAAPH/
MetalLB `api/` packages into `api/external/` (excluding webhooks/tests) with sed-rewritten import
paths, version-pinned by tiny throwaway modules under `hack/third-party/` (`make/apis.mk:4-60`).
Rationale: providers pin conflicting controller-runtime/cluster-api versions. Kubeadm and CAPD
types are exempt because they live in the cluster-api module tree itself. The repo is three Go
modules (root, `api/`, `common/`) so consumers import config types without dragging controller
dependencies; `common/` is a published reusable library.

This validates this repo's existing choices: unstructured access for `TinkerbellMachine` and
`TalosControlPlane` (C3/C5) is the lightweight end of the same spectrum, and rsync-vendoring is the
recipe if typed access to fork APIs is ever needed in one binary with cluster-api v1.13.

## 3. How the CAPI integration is set up

### The runtime server IS the webhook server

`common/pkg/server.NewWebhookServer` builds a runtime catalog (`runtimehooksv1.AddToCatalog`) and
returns `sigs.k8s.io/cluster-api/exp/runtime/server.Server` — which embeds controller-runtime's
`webhook.Server`. `main.go` assigns it to `manager.Options.WebhookServer`, so runtime hooks, four
admission webhooks, metrics, and three controllers share **one process, one listener (9443), one
cert** (`common/pkg/server/server.go:43-56`; `cmd/main.go:160-171,284-303`). Two Services
(`-runtimehooks`, `-admission`) target the same container port. The whole wrapper is ~150 lines.
`/discovery`, per-handler `failurePolicy` (default `Fail`) and `timeoutSeconds` (default 10s) are
delegated entirely to the CAPI library.

### ExtensionConfig with zero custom code

The `ExtensionConfig` is a **static Helm template** — `clientConfig.service` only, no
`namespaceSelector`, no settings — whose caBundle is injected by CAPI's *native*
`runtime.cluster.x-k8s.io/inject-ca-from-secret` annotation pointing at the cert-manager-issued
Secret (`charts/.../templates/extensionconfig.yaml:4-17`). No self-registration controller, no jobs.
One cert-manager `Certificate` carries SANs for both Services; admission webhook configs separately
use `cert-manager.io/inject-ca-from`.

Reconciliation with this repo's F8/architecture notes: CABPT's self-registration Runnable exists
because clusterctl/kustomize cannot rewrite a cluster-scoped ExtensionConfig's service namespace —
CAREN sidesteps that because **Helm templates the namespace at install time** (and its clusterctl
artifact is itself rendered from the chart at release). When Helm is the installer, the static
manifest + CAPI CA annotation is the simpler correct path; self-registration remains the answer only
for raw-kustomize distribution.

### Hooks implemented, and the blocking idioms

- Lifecycle: `BeforeClusterCreate`, `AfterControlPlaneInitialized`, `BeforeClusterUpgrade`,
  `AfterControlPlaneUpgrade` (interface exists, **zero implementers**), `BeforeClusterDelete`. No
  `AfterClusterUpgrade`, and **no in-place-update hooks anywhere** — consistent with this repo's
  constraint that the `UpdateMachine` surface is CABPT's alone.
- All addon implementations of a hook fold into **one registered handler per hook** (named `caren`)
  that fans out in parallel and aggregates: failure if any failed, messages joined, and
  `retryAfterSeconds` = lowest non-zero (`pkg/handlers/lifecycle/handlers.go:156-233`,
  `parallel.go:20-132`). CAPI sees one handler per hook; ordering/composition stays in-process.
- Blocking idiom: `Failure + SetRetryAfterSeconds(5)` as "poll me again"; success only on verified
  completion. `konnectoragent`'s `BeforeClusterDelete` is a full async state machine whose timeout
  is a **terminal blocking state without retryAfterSeconds**, demanding manual intervention
  (`konnectoragent/handler.go:642-691`). Skip-fast convention: absent config variable → immediate
  Success, so a shared hook never bricks unrelated clusters.

### Never trust the hook payload

Every lifecycle handler's first act is converting the payload's v1beta1 Cluster to v1beta2; CAPI
1.12+ **strips status from hook requests**, so handlers re-fetch the Cluster from the API server
before making decisions (`servicelbgc/handler.go:62-76`). `DiscoverVariables` responses are built in
v1beta2 and down-converted to v1beta1 for the still-v1alpha1 hooks wire format. This is the same
lesson as the CABPT fork's "read machine addresses from the API, not the hook request" fix: treat
payloads as keys, fetch authoritative state.

### Validation: admission webhooks, not ValidateTopology

`ValidateTopology` has an interface, registration plumbing — and **zero implementers**. All
validation is ordinary admission webhooks on Cluster with `failurePolicy: Fail`, scoped by CEL
`matchConditions: has(object.spec.topology)` so non-topology clusters bypass CAREN entirely
(`charts/.../templates/webhooks.yaml`), plus a pluggable preflight-check framework with per-check
skip annotations. Cross-field checks CEL cannot express (Prism Central IP vs endpoint IP) live in
webhook validators that unmarshal the same `clusterConfig` variable.

### Controllers alongside hooks — the strongest validation of this repo's architecture

Even the canonical hook-first repo runs three **plain controllers** in the same manager:
`namespacesync`, `enforceclusterautoscalerlimits`, and `failuredomainrollout`
(`cmd/main.go:187-282`) — because their triggers (namespace creation, MachineDeployment mutation by
the autoscaler, `cluster.status.failureDomains` drift) **fire no runtime hook**. Jobs driven by
resource watches stay controllers even after full ClusterClass adoption; only cluster-level
transition gating belongs in lifecycle hooks. Per-machine work (this repo's C3 pre-terminate flow)
has no lifecycle-hook equivalent at all.

### Contract tracking and clusterctl integration

- Pins: cluster-api v1.12.5, controller-runtime v0.22.5, k8s.io v0.34.8; a dedicated dependabot
  `capi-core-minor` group; dev/e2e `CAPI_VERSION` derived from `go.mod` via `go list -m` so the
  deployed CAPI always matches the compiled-against contract.
- `metadata.yaml` `releaseSeries` maps every CAREN minor to a CAPI contract (0.2–0.44 → v1beta1,
  0.45+ → v1beta2) — this is how clusterctl judges compatibility for a **RuntimeExtensionProvider**.
- goreleaser `helm template`s the chart into `runtime-extensions-components.yaml` at release, so
  `clusterctl init --runtime-extension caren:vX` and `helm install` deploy identical manifests
  (`.goreleaser.yml:28-55`). Gotcha found: the release hook still stamps contract `v1beta1`
  literally — automate the contract value from `go.mod`, not a string.
- Feature gates use `k8s.io/component-base/featuregate` behind `--feature-gates`
  (`pkg/feature/gates.go`) — standard Alpha/Beta/GA vocabulary instead of bespoke enable flags.

## 4. Deployment, addons, operations (what ships around the code)

- **Posture**: one Deployment, 1 replica, **no leader election, no PDB**, `system-cluster-critical`
  priority. With `failurePolicy: Fail` webhooks and required external patches, a down CAREN blocks
  all topology reconciliation — a real availability trade accepted because the topology controller
  retries hooks and the CEL scope limits blast radius to topology clusters.
- **Addons**: installed from lifecycle hooks via two strategies — CAAPH `HelmChartProxy`
  (server-side-applied, Cluster-owner-ref'd) or `ClusterResourceSet` — with chart name/version/repo
  for ~18 addons read at hook time from a chart-shipped ConfigMap generated from one pinned source
  of truth (`make/addons.mk` + `make addons.sync`). Install at `AfterControlPlaneInitialized`,
  re-apply at `BeforeClusterUpgrade` (which can *block* the upgrade until addons reconcile — a
  guarantee plain controllers cannot give), uninstall by deleting the proxy in `BeforeClusterDelete`.
- **Cluster identity**: a UUIDv7 stamped once onto each Cluster by CAREN's own mutating webhook
  (`caren.nutanix.com/cluster-uuid`); all per-cluster derived resources (Helm releases) key on it,
  not the cluster name — immune to delete-and-recreate-same-name collisions.
- **Air gap**: `mindthegap` helm-chart bundles baked into a scratch image at release,
  initContainer-copied onto a PVC, served in-cluster as an OCI repo; every addon `RepositoryURL`
  helm-flips between upstream and the mirror; **N−2 old bundles kept** so older clusters' pinned
  chart versions stay resolvable.
- **Release engineering**: chart version == appVersion == git tag stamped at `helm package` time
  over in-tree `v0.0.0-dev` placeholders; Helm repo index on GitHub Pages pointing at release
  assets; release-please tags root + `api/` + `common/` modules.
- **Testing pyramid**: table-driven `GeneratePatches` unit tests via `common/pkg/testutils/capitest`
  (fixture request items + JSON-patch matchers, **no envtest**); envtest reserved for admission
  webhooks and controllers; chart-testing lint-and-install on kind; e2e on the CAPI framework where
  CAREN registers **itself** as a provider with two versions — the previous GitHub release
  (upgrade-from) and vNext from local source — so every PR exercises the real upgrade path.

### Verified defects worth remembering (so we don't copy them)

- `CreateClusterGetter`'s `sync.Once` is declared inside the closure body, re-initializing per call
  — the memoization is broken and every mutator re-fetches the Cluster (`meta.go:66-83`).
- The `-acpu` registration branch forgot `strings.ToLower` (`server.go:101`) — benign only because
  nothing implements that hook; derive registered names through one helper.
- `AfterControlPlaneUpgrade` dispatch exists but matches nothing — dead surface.
- The registry addon's cert renewal hangs off `BeforeClusterUpgrade`, creating a documented
  operational invariant that "clusters will be upgraded at least once every 2 years" — an example
  of a lifecycle-hook dependency quietly becoming a cadence requirement.

## 5. Lessons for this repo

### Validated: the current architecture is right

CAREN independently confirms the mechanism decision in [architecture.md](architecture.md): watches
→ controllers, synchronous kubectl-visible rejection → admission webhooks (`ValidateTopology`
unused even there), per-machine work → no hook exists. Nothing in CAREN suggests the no-ClusterClass
design should have used Runtime SDK machinery.

### Adopt now (no ClusterClass required)

1. **CEL `matchConditions` on webhooks**: scope C2's Workflow-CREATE webhook (and any future
   Cluster webhook) with CEL conditions so `failurePolicy: Fail` is safe; the inverse guard
   (`!has(object.spec.topology)`) protects current machinery during any future incremental
   migration where topology and non-topology clusters coexist.
2. **Immutable identity annotation**: stamp a UUIDv7 on Clusters (or Machines) at CREATE and key
   derived resources (Workflows, rufio Jobs, cached secrets) on it — kills
   delete-and-recreate-same-name collisions like the known `<name>-poweroff` rufio Job hazard.
3. **capitest-style handler tests**: table-driven request/response fixtures with JSON-patch/field
   matchers for webhook and future handler logic; keep envtest for what actually needs an API
   server (the split this repo already practices).
4. **`component-base` feature gates** for opt-in behavior (C2's require-classified-marker check,
   future experimental flows) instead of bespoke flags.
5. **Release plumbing for the forks**: `helm template` → components.yaml + `metadata.yaml`
   releaseSeries makes CABPT/CACPPT/CAPT forks clusterctl- and CAPI-operator-manageable; automate
   the contract value from `go.mod`.
6. **Pin-once addon/version bookkeeping**: one make include with `make sync` regeneration for
   anything version-pinned in multiple places (the P7 artifact-name pin, Talos versions in
   docs/tests).
7. **Air-gap pattern** (`mindthegap` bundles + in-cluster OCI mirror + N−2 retention) extends
   naturally to HookOS/Talos image mirroring for this fleet.

### For the ClusterClass future (architecture.md "Future work")

1. **Schema-from-embedded-CRD variables**: define `TalosClusterConfig`/`TalosWorkerNodeConfig` Go
   types, controller-gen → `go:embed` → `SchemaFromCRDYAML`; ship ONE required `clusterConfig` and
   ONE optional `workerConfig` variable. CEL validation/defaults/immutability come free from core
   CAPI. Budget the `yq` post-processing step for Quantity/IntOrString fields.
2. **Version `-gp` handler names from day one** (serve N and N−1 concurrently); keep `-dv` names
   stable. Renaming a referenced handler bricks topology reconciliation; here, output drift also
   feeds CABPT's config hash.
3. **One aggregated handler per lifecycle hook** with parallel fan-out and lowest-non-zero
   `retryAfterSeconds`; sequence order-sensitive work in the list, don't parallelize it.
4. **Add the runtime server to the existing manager** (`manager.Options.WebhookServer`) — hooks
   become additive to C2's webhook infrastructure, not a second deployment. Keep leader election in
   mind: CAREN runs 1 replica without it; this repo's controllers need it, so co-hosting with
   replicas > 1 requires care.
5. **EKS-style Talos flavor**: bootstrap-agnostic mutator reuse + `TalosControlPlaneTemplate`/
   `TalosConfigTemplate` selectors via parameterized constructors; the kubeadm-shaped `generic`
   flavor is not reusable for Talos.
6. **Static helm-templated ExtensionConfig + `inject-ca-from-secret`** when Helm installs it;
   CABPT-style self-registration only for raw-kustomize distribution.
7. **`MutateIfApplicable`** (unstructured walk → selector gate → typed mutate → RFC6902 diff →
   re-apply) for any GeneratePatches handler, plus dual-contract holder-ref matching.
8. **Additive integration**: ship named handlers any user ClusterClass can reference; this repo's
   own ClusterClasses are optional convenience, never a hard dependency.

### Deliberately do not copy

- **Addon selection via topology variables** — meaningless without ClusterClass; addon/config
  intent here stays in CRDs/annotations (C1's contracts).
- **1 replica + `failurePolicy: Fail` + no PDB** as an unexamined default — this repo documents
  blast radius per webhook (C2's objectSelector scoping and disabled-by-default webhook are the
  local answer).
- **Wildcard RBAC over all provider groups** — CAREN needs it only for its namespacesync feature;
  without that feature, don't inherit the ClusterRole shape.
- **Kind-substring provider dispatch** (the CCM handler) — use explicit labels/annotations, as C1
  already does.
- **Untested chart value paths** (`.Values.env` rendering) — ct lint-and-install only exercises
  what CI sets; keep chart surfaces minimal, as the existing charts do.
