# Must not trail go.mod: one module now serves both the agent and the gate, and
# the gate's dependencies set the floor. A stale tag here fails the build with
# "go.mod requires go >= 1.26.0 (running go 1.25.x; GOTOOLCHAIN=local)".
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

COPY --from=build /out/bosun /usr/local/bin/bosun

# The agent clones into a writable directory. Kept out of the image so the
# chart can mount an emptyDir and keep the root filesystem read-only.
RUN adduser -D -u 10001 agent && mkdir -p /work && chown 10001 /work
USER 10001
ENV CLONE_ROOT=/work
WORKDIR /work

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/bosun"]
