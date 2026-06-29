BINARY     := kscribe
CMD        := ./cmd/kscribe
MODULE     := github.com/amjadjibon/kscribe

# Use go run with the tools.go-pinned versions (build tag: tools).
CONTROLLER_GEN := go run -tags tools sigs.k8s.io/controller-tools/cmd/controller-gen
TEMPL          := go run -tags tools github.com/a-h/templ/cmd/templ

.PHONY: build test generate manifests templ vet manifest-check

build:
	go build -o bin/$(BINARY) $(CMD)

test:
	go test ./...

vet:
	go vet ./...

# Generate deep-copy and other object code. Scans ./... so it's a clean no-op in phase 1.
generate:
	$(CONTROLLER_GEN) object:headerFile="" paths="./..."

# Generate CRD manifests and RBAC directly into the Helm chart — the single
# source of truth. deploy/kscribe.yaml is rendered from the chart (see manifest-check).
manifests:
	$(CONTROLLER_GEN) crd rbac:roleName=kscribe-role paths="./..." output:crd:dir=charts/kscribe/crds output:rbac:dir=charts/kscribe/templates

templ:
	$(TEMPL) generate

# Verify deploy/kscribe.yaml is reproducible: rebuild it and assert no diff (TASK-038).
manifest-check:
	bash scripts/build-manifest.sh
	git diff --exit-code deploy/kscribe.yaml
