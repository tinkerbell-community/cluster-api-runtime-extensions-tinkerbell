# Test CRDs

Copies of the Tinkerbell CRDs the envtest suite installs, taken from
`github.com/tinkerbell/tinkerbell` `crd/bases/v1alpha1/` (the source of the
`github.com/tinkerbell/tinkerbell/api` types this controller writes). Refresh
them when bumping the api dependency:

- `tinkerbell.org_hardware.yaml`
- `bmc.tinkerbell.org_machines.yaml`

The `amt.tinkerbell.org_*.yaml` CRDs are this repo's own, generated from
`api/amt/v1alpha1`. Refresh them with `make generate` and copy from
`api/amt/v1alpha1/crds/`.
