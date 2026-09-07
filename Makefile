REGISTRY ?= ghcr.io/tinkerbell-community

.PHONY: build
build:
	CGO_ENABLED=0 go build -o bin/runtime-extensions ./cmd/runtime-extensions
	CGO_ENABLED=0 go build -o bin/bmc-discovery ./cmd/bmc-discovery

.PHONY: test
test:
	CGO_ENABLED=1 go test -race -coverprofile=cover.out ./...

# Field-ownership tests against a real API server (envtest). Managed-fields
# semantics are asserted here, not with the fake client — see
# docs/discovery-field-ownership.md.
ENVTEST_K8S_VERSION ?= 1.37.0
.PHONY: test-envtest
test-envtest:
	KUBEBUILDER_ASSETS="$$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.24 use $(ENVTEST_K8S_VERSION) -p path)" \
		CGO_ENABLED=1 go test -race -tags envtest -run 'TestEnvtest' ./internal/sync/... ./internal/janitor/... ./internal/resolve/... ./internal/upgrade/...

.PHONY: vet
vet:
	go vet ./...

# Regenerate deepcopy + the embedded (never-installed) variable CRDs after
# editing api/v1alpha1 types. The schema tests fail if the output drifts.
CONTROLLER_GEN_VERSION ?= v0.19.0
.PHONY: generate
generate:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) object paths="./api/v1alpha1/..."
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) crd paths="./api/v1alpha1/..." output:crd:artifacts:config=api/v1alpha1/crds
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) object paths="./api/amt/v1alpha1/..."
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) crd paths="./api/amt/v1alpha1/..." output:crd:artifacts:config=api/amt/v1alpha1/crds

.PHONY: fmt-check
fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

.PHONY: lint
lint:
	golangci-lint run

# Build binaries and container images locally via goreleaser without
# publishing or signing anything.
.PHONY: snapshot
snapshot:
	goreleaser release --clean --snapshot --skip=sign,publish

.PHONY: helm-lint
helm-lint:
	@for chart in charts/*/; do helm lint "$$chart" || exit 1; done

# The single clusterctl RuntimeExtensionProvider artifact: both Deployments
# (runtime-extensions + bmc-discovery) rendered from their charts into one
# components file (spec §2.7). Namespace is templated at render time, matching
# what `clusterctl init --runtime-extension` would apply.
COMPONENTS_NAMESPACE ?= tinkerbell-system
.PHONY: components
components:
	@mkdir -p dist
	helm template cluster-api-runtime-extensions-tinkerbell charts/cluster-api-runtime-extensions-tinkerbell \
		--namespace $(COMPONENTS_NAMESPACE) > dist/runtime-extensions-components.yaml
	@echo "---" >> dist/runtime-extensions-components.yaml
	helm template tinkerbell-bmc-discovery-controller charts/tinkerbell-bmc-discovery-controller \
		--namespace $(COMPONENTS_NAMESPACE) >> dist/runtime-extensions-components.yaml
	@echo "rendered dist/runtime-extensions-components.yaml"

# metadata.yaml sanity: the clusterctl contract must match the cluster-api
# minor actually compiled against (CAREN's documented literal-drift bug).
.PHONY: verify-contract
verify-contract:
	@capi="$$(go list -m -f '{{.Version}}' sigs.k8s.io/cluster-api)"; \
	grep -q "cluster-api $$capi" metadata.yaml || \
		{ echo "metadata.yaml contract comment is stale (cluster-api is $$capi)"; exit 1; }
