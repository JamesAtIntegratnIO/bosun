# Must not trail go.mod: one module now serves both the agent and the gate, and
# the gate's dependencies set the floor. A stale tag here fails the build with
# "go.mod requires go >= 1.26.0 (running go 1.25.X; gotoolchain=local)".
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/bosun .

FROM alpine:3.21
# git is a real runtime dependency: the agent clones the pull request's branch
# and pushes the fix as an ordinary commit.
RUN apk add --no-cache ca-certificates git

# helm, for the same reason the gate carries it: the structural migration needs
# the schema a chart ships at the version being promoted to, and the only thing
# guaranteed to render a chart the way the cluster's own Helm will is Helm. A
# library pinned here would be a slower way to drift away from that.
#
# Same version as the gate's image, deliberately, two components rendering the
# same chart with different Helms is a difference nobody would think to look
# for.
ARG HELM_VERSION=v3.19.0
# No default value. Targetarch is a built-in BuildKit arg, and assigning one
# here shadows what BuildKit injects, which is how the gate's arm64 image once
# came to download amd64 helm and fail exec'ing it under emulation. The
# fallback is computed inside run instead, so BuildKit stays authoritative
# where it sets the arg and a plain `docker build` still resolves natively.
ARG TARGETARCH
# kubeconform joined helm when the gate moved in-process: an agent gating in
# cluster mode runs the same schema validation the CI adapter ran, and a
# missing binary would silently mean "validate: enabled" validates nothing.
# Same version as the gate's image, same reason as helm.
ARG KUBECONFORM_VERSION=v0.7.0
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

COPY --from=build /out/bosun /usr/local/bin/bosun

# The agent clones into a writable directory. Kept out of the image so the
# chart can mount an emptyDir and keep the root filesystem read-only.
RUN adduser -D -u 10001 agent && mkdir -p /work && chown 10001 /work
USER 10001
ENV CLONE_ROOT=/work
# helm needs somewhere to put its cache and uid 10001 has no home directory --
# the same accommodation the gate's CI job makes when it overrides the user.
ENV HOME=/work
WORKDIR /work

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/bosun"]
