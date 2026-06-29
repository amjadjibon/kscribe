# syntax=docker/dockerfile:1

# --- build stage ---
FROM golang:1.26 AS build
WORKDIR /src

# Cache deps separately from source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# modernc.org/sqlite is pure Go, so a static CGO-free binary works.
# Generated code (deepcopy, templ) is committed, so no codegen needed here.
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/kscribe ./cmd/kscribe

# --- runtime stage ---
# distroless static:nonroot runs as UID 65532, matching the Deployment's
# runAsNonRoot + fsGroup: 65532 (config/manager/deployment.yaml).
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /out/kscribe /kscribe
USER 65532:65532
ENTRYPOINT ["/kscribe"]
