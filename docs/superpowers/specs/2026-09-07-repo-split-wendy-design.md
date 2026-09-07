# Repo split: extract the BMC discovery controller as `wendy`

Design date: 2026-09-07.

## Problem

`cluster-api-runtime-extensions-tinkerbell` currently holds two unrelated
products in one Go module:

1. **The CAPI runtime extension** — `cmd/runtime-extensions`, a consolidated
   manager (resolver, Workflow-CREATE gate, teardown, janitor,
   upgrade-coordinator) plus the Runtime SDK hook handlers under `pkg/`. It
   ships as a clusterctl `RuntimeExtensionProvider`.
2. **The mDNS BMC discovery controller** — `cmd/bmc-discovery`, which browses
   mDNS, collects Redfish inventory, and syncs Tinkerbell `Hardware` /
   `bmc.tinkerbell.org` `Machine` resources.

The second is about to grow well past discovery: an outbound zero-touch
provisioning endpoint (Intel vPro/AMT remote configuration), SSDP-based device
onboarding, BMC credential bootstrapping, and its own CRDs for inventory and
provisioning state. None of that is CAPI-adjacent — it is a device-management
plane that happens to terminate in Tinkerbell `Hardware` creation. Keeping it
inside a repo published as a CAPI provider misrepresents both halves and forces
one dependency set, one release cadence, and one CI budget on two audiences.

This spec covers **only the structural split**. It adds no features to either
side.

## Decisions

| Decision | Choice | Rationale |
| --- | --- | --- |
| New repo name | `tinkerbell-community/wendy` | On-theme with the project's Peter Pan naming (`tinkerbell`, `rufio`, `tootles`, `smee`, `hook`). Wendy takes stock of the Lost Boys, names them, and arranges their adoption — discovery → inventory → identity → handoff to Tinkerbell as `Hardware`. |
| Which side keeps the existing repo | The runtime extension | Avoids a second GitHub rename and avoids re-doing the module-path rewrite that already landed across 30 commits. |
| `internal/janitor`, `internal/resolve` | Stay with the runtime extension | Both are `Hardware`-facing, but they exist to serve the CAPI lifecycle (post-release scrub; per-machine image identity for CAPT). Moving them would couple `wendy` to the CAPI contract. |
| `internal/logging` | Duplicated into both repos | 33 lines, stdlib-only. A shared module would cost more than it saves. |
| History for `wendy` | Branch at `4d4bc22` + two forward-port commits | See below. No `git filter-repo` required. |
| Artifact names | Renamed to `wendy` | The binary, chart, image, and Deployment all become `wendy`. Requires one redeploy; doing it now, with a single known consumer, is cheaper than later. |
| Nana overlap | Out of scope | See "Open questions". |

## The split line

The internal import graph already forms two disjoint subgraphs. The only shared
node is `internal/logging`. No package changes sides.

### `wendy` (new repo)

Paths below name the **current** tree. `wendy` is built from `4d4bc22`, where the
same content lives at `cmd/main.go` and `helm/` — the "Note" column gives the
final destination, and Commit C performs the moves.

| Path | Note |
| --- | --- |
| `cmd/bmc-discovery/main.go` | Becomes `cmd/wendy/main.go` |
| `internal/mdns` | mDNS browser |
| `internal/inventory` | Redfish collection via bmclib |
| `internal/sync` | `Hardware` / `Machine` / Secret sparse SSA |
| `internal/controller` | Runnable wiring the three together |
| `internal/logging` | Copy |
| `charts/tinkerbell-bmc-discovery-controller` | Becomes `charts/wendy` |
| `test/crds/bmc.tinkerbell.org_machines.yaml` | Only `internal/sync` uses the `bmc` API group |
| `test/crds/tinkerbell.org_hardware.yaml` | Copy; both repos need it |
| `test/crds/README.md` | Copy |
| `docs/discovery-field-ownership.md` | The C0 sparse-SSA rationale |
| `docs/superpowers/specs/2026-08-30-bmc-discovery-controller-design.md` | |
| `docs/superpowers/plans/2026-08-30-bmc-discovery-controller.md` | |

### `cluster-api-runtime-extensions-tinkerbell` (existing repo)

Keeps `cmd/runtime-extensions`, `internal/{resolve,teardown,janitor,upgrade,logging}`,
`pkg/handlers/**`, `pkg/variables`, `api/v1alpha1/**`,
`charts/cluster-api-runtime-extensions-tinkerbell`, `metadata.yaml`,
`test/crds/tinkerbell.org_{hardware,workflows}.yaml`, and all remaining `docs/`.

### Dependency consequences

Direct dependencies partition cleanly. Leaving the extension:
`bmc-toolbox/bmclib/v2`, `bmc-toolbox/common`, `jacobweinstock/registrar`,
`libp2p/zeroconf/v2`, `stmcginnis/gofish`.

Never entering `wendy`: `sigs.k8s.io/cluster-api`,
`siderolabs/talos/pkg/machinery`, `siderolabs/go-kubernetes`,
`cosi-project/runtime`, `blang/semver/v4`, `k8s.io/apiextensions-apiserver`,
`k8s.io/component-base`, `prometheus/client_golang`, `sigs.k8s.io/yaml`.

Shared by both: `tinkerbell/tinkerbell/api`, `k8s.io/{api,apimachinery,client-go,utils}`,
`sigs.k8s.io/controller-runtime`, `go-logr/logr`.

## History strategy

`4d4bc22` ("feat: record only the primary ethernet interface on Hardware") is
the last commit before the repo re-scoped. At that commit the tree is *already*
a complete, standalone BMC discovery controller: all five internal packages, the
Helm chart, BMC-only CI, goreleaser, Dockerfile, README, Makefile, and both
superpowers docs. The next commit, `170bc0a`, adds only `CLAUDE.md` and begins
the re-scope.

So `wendy`'s history is `e6f9182..4d4bc22` — **29 commits, unrewritten** — plus
forward ports. `git filter-repo` is not needed and is not used.

Only four post-pivot commits touch BMC paths, and their disposition is:

| Commit | Disposition |
| --- | --- |
| `499bec7` fix: migrate resource sync to sparse server-side apply (C0) | **Cherry-pick.** Every file it touches is BMC-side. |
| `e5a3bcc` docs: re-scope repo as glue (design set) | **Partial** — take `docs/discovery-field-ownership.md` only. |
| `0f7d6e3` feat!: re-scope to cluster-api-runtime-extensions-tinkerbell (SP-0) | **Skip.** Module rename; `wendy` gets its own. |
| `3b2e380` feat(resolve): Workflow-CREATE admission gate (SP-4) | **Skip.** Only added the Workflows CRD fixture, which is extension-only. |

`499bec7` cherry-picks cleanly onto `4d4bc22`: nothing between them touches
`internal/sync` (`170bc0a` is CLAUDE.md-only, `e5a3bcc` is docs-only), and it
predates the module rename so its import paths still match. `controller-runtime
v0.24.1` is already a direct dependency at `4d4bc22`, so the envtest it adds
compiles without a `go.mod` change.

The chart is **byte-identical** between `4d4bc22` and `HEAD`. The only
substantive code delta on the BMC side since the pivot is `internal/sync`; the
changes in `internal/controller` and `internal/inventory` are import-path churn
from `0f7d6e3` alone.

### `wendy` construction

1. `git branch` at `4d4bc22` in a fresh working directory — 29 commits.
2. **Commit A**: cherry-pick `499bec7`. Brings the sparse-SSA rewrite,
   `internal/sync/envtest_test.go`, the three `test/crds` fixtures, the
   `test-envtest` Makefile target, and the CI step.
3. **Commit B**: add `docs/discovery-field-ownership.md` from `e5a3bcc`.
4. **Commit C**: rename to `wendy` —
   - module path → `github.com/tinkerbell-community/wendy`
   - `cmd/main.go` → `cmd/wendy/main.go`
   - `helm/` → `charts/`, chart directory → `charts/wendy`
   - chart name, `Chart.yaml` metadata, image repository, Deployment /
     ServiceAccount / RBAC names, goreleaser build id and image path, Dockerfile
     `LICENSE` doc path, and the `helm-lint` glob in the Makefile → `wendy`
   - new `CLAUDE.md` scoped to `wendy` (none exists at `4d4bc22`)
   - `README.md` updated for the new name and the incoming provisioning scope

### Extension repo removal

One commit on a branch off `runtime-extensions` (**not** `main` — `main` is 22
commits behind and predates the entire re-scope):

- delete `cmd/bmc-discovery`, `internal/{mdns,inventory,sync,controller}`,
  `charts/tinkerbell-bmc-discovery-controller`,
  `test/crds/bmc.tinkerbell.org_machines.yaml`, and the three BMC docs
- `.goreleaser.yaml`: drop the `bmc-discovery` build and its `dockers_v2` image
- `Makefile`: drop the `bmc-discovery` build line, the `bmc-discovery` chart
  append in `components`, and `./internal/sync/...` from `test-envtest`
- `Dockerfile`: correct the `tinkerbell-bmc-discovery-controller` LICENSE path
- `go mod tidy` to drop the five BMC-only dependencies
- `CLAUDE.md` and `README.md`: remove the BMC discovery sections and point at
  `wendy`

## Verification

Neither repo is pushed until its own checks pass.

Both repos independently:

- `make build`
- `make test`
- `make test-envtest`
- `make helm-lint`
- `go mod tidy` produces no diff
- `go list ./...` shows no import of the other module

Extension repo additionally: `make components` and `make verify-contract`.

`wendy` additionally: `git log --oneline | wc -l` reports 32, and
`git log --follow internal/mdns/browser.go` reaches `ce930fc`, proving history
survived.

## Sequencing

1. Build `wendy` locally in a fresh directory from `4d4bc22`; forward-port,
   rename, verify green. Nothing is pushed yet.
2. Create `github.com/tinkerbell-community/wendy` and push. Creating it does not
   disturb the `tinkerbell-bmc-discovery-controller` →
   `cluster-api-runtime-extensions-tinkerbell` redirect, since the new repo takes
   a third name.
3. Remove the BMC side from the extension repo on a branch; verify green; merge.
4. Delete the stale duplicate working copy at
   `~/src/tinkerbell-community/tinkerbell-bmc-discovery-controller`. It is a
   second full clone of the same history whose `origin` is the pre-rename URL,
   and leaving it invites committing to the wrong tree.
5. Redeploy: `helm uninstall tinkerbell-bmc-discovery-controller` and install
   `charts/wendy` from the new repo.

## Out of scope

- Any vPro/AMT, SSDP, Redfish-serving, or CRD work in `wendy`. The split lands
  the existing discovery controller under a new name and nothing more.
- Changes to `janitor`, `resolve`, `teardown`, or `upgrade` behaviour.
- Merging `main` in the extension repo, or any decision about the unpushed
  commit on `runtime-extensions`.

## Open questions

**`nana` overlap.** `tinkerbell-community/nana` is an existing Go service doing
BMC emulation/stitching behind a Redfish v1 API and JSON-RPC endpoint, with a
pluggable provider registry (JetKVM, UniFi PoE). It is reportedly no longer in
use and may be repurposed. Its Redfish-serving layer and provider registry
overlap with `wendy`'s planned AMT-to-Redfish compatibility layer — an AMT
provider in that registry is a plausible home for it.

Deliberately unresolved here: this split is structural, and folding `nana` into
`wendy` (or the reverse) is a separate design with its own migration. Revisit
before starting the provisioning work, not before the split.
