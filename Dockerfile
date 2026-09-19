# syntax=docker/dockerfile:1

# Build on the host's native platform and cross-compile, so multi-arch builds
# don't need emulation.
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/markdown-publish .

# No shell, no package manager, no OS: the binary is the whole image. It makes
# no outbound connections, so it needs no CA certificates either.
FROM scratch
COPY --from=build /out/markdown-publish /markdown-publish
USER 65534:65534
ENV VAULT_DIR=/vault LISTEN=:8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/markdown-publish", "-healthcheck"]
ENTRYPOINT ["/markdown-publish"]
