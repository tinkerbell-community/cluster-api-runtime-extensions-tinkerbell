# ClusterClass adoption runbook (Wave 2, SP-6)

The ordered, operational half of [runtime-extensions-migration.md](runtime-extensions-migration.md)
§4.2/§7.2: migrating the live terraform-created cluster onto `spec.topology` **without rolling a
control-plane machine**. Every gate here is mandatory; the code side (variables, runtime server,
flavor, ExtensionConfig) ships in the chart behind `clusterClass.enabled` and does nothing until
these steps are taken deliberately.

## Hard prerequisites (do not start without all three)

1. **etcd at 3 members (DECIDED, §9 Q16).** Grow the control plane to a fault-tolerant 3-member
   etcd first — add a third CP machine via the current non-topology path, or adopt the bootstrap
   node (`replicas` `n-1 → n`). A 2-member etcd has zero fault tolerance; a single unexpected CP
   roll during adoption can lose quorum. A tested etcd snapshot + restore runbook is a backstop,
   not a substitute.
2. **Core CAPI feature gates.** The core manager must run `ClusterTopology=true` **and**
   `RuntimeSDK=true` (RuntimeSDK is already on for the in-place hooks; the incremental change is
   `ClusterTopology`). Without RuntimeSDK the external DiscoverVariables/GeneratePatches hooks are
   inert, not merely unused.
3. **Fork CRDs installed** at the versions the flavor references: `TalosControlPlaneTemplate`
   (controlplane/v1beta1), `TalosConfigTemplate` (bootstrap/v1beta1), `TinkerbellMachineTemplate`
   and `TinkerbellClusterTemplate` (infrastructure/v1beta2 — the ClusterClass-support CAPT branch).

## Step 1 — enable the extension's topology surface

```sh
helm upgrade <release> charts/cluster-api-runtime-extensions-tinkerbell \
  --reuse-values --set clusterClass.enabled=true
```

This ships the ExtensionConfig (CA injected from the serving-cert Secret; the runtime server
already answers on every replica) and the flavor objects. Verify discovery:
`kubectl get extensionconfig cluster-api-runtime-extensions-tinkerbell -o yaml` must show the
handlers under `status.handlers` (one `-dv`, two `-gp`) with no errors.

> Multiple ExtensionConfigs are expected: the bootstrap provider's in-place extension keeps its
> own. Only a second `UpdateMachine` handler would be fatal, and this server registers none
> (build-guarded).

## Step 2 — no-op adoption rehearsal (throwaway cluster)

Author the `Cluster.spec.topology` for the live cluster's *current* state: `topology.classRef` →
the flavor's ClusterClass, `topology.version` = the running Kubernetes version,
`topology.controlPlane.replicas` = current replicas, `variables.clusterConfig.talos.version` = the
**running** Talos minor (pin it — never `latest` — so the first reconcile computes today's state),
`variables.clusterConfig.controlPlaneEndpoint` = the live VIP.

In a throwaway management cluster with the same CRDs and this extension, create the same objects
and diff what the topology controller renders against the live specs until the diff is **empty**.
The adoption gate is *empty diff*: patch drift feeds the bootstrap provider's in-place config hash,
so a non-empty diff means node reboots on the real cluster.

## Step 3 — pause, flip, verify, unpause (live cluster)

1. `kubectl annotate cluster <name> cluster.x-k8s.io/paused=true` (and pause MHC if configured).
2. Add `spec.topology` to the **existing** Cluster object (adoption, never delete-and-recreate).
   The topology controller adopts the live `TalosControlPlane`/`TinkerbellMachineTemplate`/
   `TalosConfigTemplate` by name.
3. Inspect the queued plan while paused: no template rotation, no version change, no pending
   rollout may appear.
4. Unpause only after confirming zero rollout is pending.

## Step 4 — terraform handoff (`state rm`, never a config delete)

The live CP objects are terraform-managed `kubectl_manifest` resources and the `TalosControlPlane`
carries a finalizer: removing them from terraform config would DELETE them, and deleting the
TalosControlPlane triggers the control-plane provider's finalizer teardown of every CP machine —
cluster loss. The only safe sequence (§7.2):

```sh
terraform state rm 'module.cluster.kubectl_manifest.talos_control_plane' \
                   'module.cluster.kubectl_manifest.machine_template' \
                   'module.cluster.kubectl_manifest.tinkerbell_cluster'   # adjust addresses
```

then remove those resources from the config **and** move `spec.topology` authorship into
terraform's `Cluster` manifest in the same change, so terraform stops stripping the
operator-added topology on its next apply.

## Rollback

Remove `spec.topology` (CAPI supports leaving topology; adopted objects become standalone again)
— rehearse this in the throwaway cluster too. Because the CP resources were handed off with
`terraform state rm`, rollback re-imports them into terraform state; it must never re-add them to
config in a way that re-runs a create/delete, or the finalizer-teardown hazard reappears.

## After adoption

A Talos version bump is `variables.clusterConfig.talos.version` (SP-7's GeneratePatches injects
the version; the per-machine installer image stays on the Hardware-annotation path — Strategy B,
so the cutover changes no running machine's image). Keep `templateInline` on the pre-existing
non-topology templates untouched; only the flavor's *new* templates use `templateRef`.
