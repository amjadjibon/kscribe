BINARY     := kscribe
CMD        := ./cmd/kscribe
MODULE     := github.com/amjadjibon/kscribe

# Use go run with the tools.go-pinned versions (build tag: tools).
CONTROLLER_GEN := go run -tags tools sigs.k8s.io/controller-tools/cmd/controller-gen
TEMPL          := go run -tags tools github.com/a-h/templ/cmd/templ

.PHONY: build test generate manifests templ vet

build:
	go build -o bin/$(BINARY) $(CMD)

test:
	go test ./...

vet:
	go vet ./...

# Generate deep-copy and other object code. Scans ./... so it's a clean no-op in phase 1.
generate:
	$(CONTROLLER_GEN) object:headerFile="" paths="./..."

# Generate CRD manifests and RBAC. Scans ./... so it's a clean no-op in phase 1.
manifests:
	$(CONTROLLER_GEN) crd rbac:roleName=kscribe-role paths="./..." output:crd:dir=config/crd/bases

templ:
	$(TEMPL) generate
