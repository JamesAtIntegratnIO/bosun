# The gate shells out to helm and kubeconform rather than vendoring their
# libraries: chart rendering has to match what the cluster's own Helm does, and
# pinning a library version is a slower way to drift away from that. Pinned
# to the build platform and cross-compiled, rather than emulating this stage
# once per target architecture. Go cross-compiles natively, so this is the
# difference between one fast build and one very slow one, measured at 336s
# emulated against a fraction of that cross-compiled.
#
# This requires buildkit. The legacy builder does not substitute $buildplatform,
# so the flag becomes `--platform=` and the build dies with a opaque "invalid OS
# component" error. If you hit that, you are on the legacy builder:
#
#     brew install docker-buildx
#     mkdir -p ~/.docker/cli-plugins
#     ln -sf /opt/homebrew/lib/docker/cli-plugins/docker-buildx \
#            ~/.docker/cli-plugins/docker-buildx
#
# then build with `docker buildx build`. CI always uses buildx.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETARCH
# Build context is the repository root, not this directory: the gate shares one
# Go module with the agent, so go.mod lives a level up.
#   docker build -f gate/Dockerfile .
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# The whole module, not a hand-picked list. This used to copy only gate/, and
# the day the gate first imported a sibling package (migrate, shared with the
# agent so the two cannot drift) the image stopped building while `go build
# ./...` in CI stayed green, CI builds the checkout, the image builds the
# copy list, and only one of them follows the import graph. v0.9.0's gate
# image died exactly that way. The agent's Dockerfile already copies the
# module wholesale for the same reason.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH:-$(go env GOARCH)}" \
    go build -trimpath -ldflags='-s -w' -o /out/gitops-gate ./gate/cmd/gitops-gate

FROM alpine:3.21
RUN apk add --no-cache ca-certificates git bash

ARG HELM_VERSION=v3.19.0
ARG KUBECONFORM_VERSION=v0.7.0
# No default value. Targetarch is a built-in BuildKit arg, and assigning one
# here shadows the value BuildKit injects, which made the arm64 image download
# amd64 helm and kubeconform, then fail exec'ing them under emulation.
#
# The fallback below is computed inside run instead, so BuildKit stays
# authoritative when it sets the arg, and a plain `docker build` without
# buildx (which injects nothing) still resolves to the native architecture
# rather than fetching `helm-...-linux-.tar.gz` and failing on `tar: invalid
# magic`.
ARG TARGETARCH

RUN set -eux; \
    arch="${TARGETARCH:-$(apk --print-arch | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')}"; \
    wget -qO- "https://get.helm.sh/helm-${HELM_VERSION}-linux-${arch}.tar.gz" \
      | tar -xz -C /tmp; \
    mv "/tmp/linux-${arch}/helm" /usr/local/bin/helm; \
    rm -rf /tmp/linux-*; \
    wget -qO- "https://github.com/yannh/kubeconform/releases/download/${KUBECONFORM_VERSION}/kubeconform-linux-${arch}.tar.gz" \
      | tar -xz -C /usr/local/bin kubeconform; \
    helm version --short; \
    kubeconform -v

COPY --from=build /out/gitops-gate /usr/local/bin/gitops-gate

# The gate only ever reads.
RUN adduser -D -u 10001 gate
USER 10001

ENTRYPOINT ["/usr/local/bin/gitops-gate"]
CMD ["--help"]
