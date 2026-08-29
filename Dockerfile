# Must not trail go.mod: one module now serves both the agent and the gate, and
# the gate's dependencies set the floor. A stale tag here fails the build with
# "go.mod requires go >= 1.26.0 (running go 1.25.X; gotoolchain=local)".
#
# Tag and digest, both. The tag is what a person reads and what Dependabot
# matches on; the digest is the part that cannot move. `golang:1.26-alpine` is
# republished whenever the base or the patch release changes, so a tag alone
# means two builds of the same commit can compile against different toolchains
# with nothing in the repository to show for it -- the same "the image changed
# underneath you" failure charts/bosun's image helper exists to prevent for the
# image this file produces.
FROM golang:1.27-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/bosun .

# 3.24 rather than 3.21, which reaches end of support in November 2026 and
# stops receiving the security backports that are most of the reason to run a
# distribution at all. Digest-pinned for the same reason as the build stage.
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
# git is a real runtime dependency: the agent clones the pull request's branch
# and pushes the fix as an ordinary commit.
RUN apk add --no-cache ca-certificates git

# helm, because chart rendering has to match what the cluster's own Helm does:
# the gate renders charts at both sides of a bump, the structural migration
# needs the schema a chart ships at the version being promoted to, and the
# only thing guaranteed to render a chart the way the cluster's own Helm will
# is Helm. A library pinned here would be a slower way to drift away from that.
ARG HELM_VERSION=v3.19.0
# No default value. Targetarch is a built-in BuildKit arg, and assigning one
# here shadows what BuildKit injects, which is how an arm64 build once came to
# download amd64 helm and fail exec'ing it under emulation. The fallback is
# computed inside run instead, so BuildKit stays authoritative where it sets
# the arg and a plain `docker build` still resolves natively.
ARG TARGETARCH
# kubeconform joined helm when the gate moved in-process: the agent runs the
# gate's schema validation itself, and a missing binary would silently mean
# "validate: enabled" validates nothing.
ARG KUBECONFORM_VERSION=v0.7.0

# The sha256 of each tarball, one per architecture because the tarballs differ
# per architecture and a single digest would be right for exactly one of them.
#
# Checked in here rather than fetched from beside the tarball. Both projects
# publish a checksum file, but it is served by the host that serves the
# tarball, so verifying against it proves only that the two agree -- which
# they would after a compromised release too. TLS was the whole integrity
# story before this, and helm is not an ordinary dependency: the gate's verdict
# *is* the output of `helm template`, so a substituted helm is a substituted
# verdict on every promotion this repository is trusted to judge.
#
# Bump a version without bumping its digests and the build stops on the
# sha256sum, which is the intended way to find out.
ARG HELM_SHA256_AMD64=a7f81ce08007091b86d8bd696eb4d86b8d0f2e1b9f6c714be62f82f96a594496
ARG HELM_SHA256_ARM64=440cf7add0aee27ebc93fada965523c1dc2e0ab340d4348da2215737fc0d76ad
ARG KUBECONFORM_SHA256_AMD64=c31518ddd122663b3f3aa874cfe8178cb0988de944f29c74a0b9260920d115d3
ARG KUBECONFORM_SHA256_ARM64=cc907ccf9e3c34523f0f32b69745265e0a6908ca85b92f41931d4537860eb83c

RUN set -eux; \
    arch="${TARGETARCH:-$(apk --print-arch | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')}"; \
    case "${arch}" in \
      amd64) helm_sha="${HELM_SHA256_AMD64}"; kubeconform_sha="${KUBECONFORM_SHA256_AMD64}" ;; \
      arm64) helm_sha="${HELM_SHA256_ARM64}"; kubeconform_sha="${KUBECONFORM_SHA256_ARM64}" ;; \
      *) echo "no pinned checksums for ${arch}; add them rather than skipping the check" >&2; exit 1 ;; \
    esac; \
    wget -qO /tmp/helm.tar.gz "https://get.helm.sh/helm-${HELM_VERSION}-linux-${arch}.tar.gz"; \
    echo "${helm_sha}  /tmp/helm.tar.gz" | sha256sum -c -; \
    tar -xzf /tmp/helm.tar.gz -C /tmp; \
    mv "/tmp/linux-${arch}/helm" /usr/local/bin/helm; \
    rm -rf /tmp/helm.tar.gz /tmp/linux-*; \
    wget -qO /tmp/kubeconform.tar.gz "https://github.com/yannh/kubeconform/releases/download/${KUBECONFORM_VERSION}/kubeconform-linux-${arch}.tar.gz"; \
    echo "${kubeconform_sha}  /tmp/kubeconform.tar.gz" | sha256sum -c -; \
    tar -xzf /tmp/kubeconform.tar.gz -C /usr/local/bin kubeconform; \
    rm -f /tmp/kubeconform.tar.gz; \
    helm version --short; \
    kubeconform -v

COPY --from=build /out/bosun /usr/local/bin/bosun

# The agent clones into a writable directory. Kept out of the image so the
# chart can mount an emptyDir and keep the root filesystem read-only.
RUN adduser -D -u 10001 agent && mkdir -p /work && chown 10001 /work
USER 10001
ENV CLONE_ROOT=/work
# helm needs somewhere to put its cache and uid 10001 has no home directory.
ENV HOME=/work
WORKDIR /work

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/bosun"]
