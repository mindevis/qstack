# OCI image of a qStack control-plane binary (spec 01 §7): a static Go
# binary on distroless Debian 13, running as a non-root user.
#   docker build -f deploy/oci/Dockerfile --build-arg BINARY=qs-iam -t … .
# Base images are pinned by digest; Dependabot updates them.
FROM golang:1.26.10-trixie AS build
ARG BINARY
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    test -n "$BINARY" && \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/mindevis/qstack/pkg/version.Version=${VERSION} -X github.com/mindevis/qstack/pkg/version.Commit=${COMMIT}" \
      -o /out/service ./cmd/${BINARY}

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
ARG BINARY
LABEL org.opencontainers.image.source=https://github.com/mindevis/qstack \
      org.opencontainers.image.licenses=Apache-2.0 \
      org.opencontainers.image.title=${BINARY}
COPY --from=build /out/service /usr/local/bin/service
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/service"]
