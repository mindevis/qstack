# qs-console image (ADR-0017): Nuxt SSR server on distroless Node 24 /
# Debian 13, running as uid 65532. Base images are pinned by digest.
#   docker build -t qs-console console/
FROM node:24.21.0-trixie-slim@sha256:173f125896c3b47ddf056734c7ea789d04595a6a08769a8f78e0df642781fb66 AS build
ARG VERSION=dev
WORKDIR /src
# pnpm version comes from package.json "packageManager".
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile
COPY . .
RUN QS_VERSION=${VERSION} NUXT_TELEMETRY_DISABLED=1 pnpm build

FROM gcr.io/distroless/nodejs24-debian13:nonroot@sha256:cb606325b7ec4af55462cb0b424a53b452a5b4a894ffe841fcf1048d216fb9f8
LABEL org.opencontainers.image.source=https://github.com/mindevis/qstack \
      org.opencontainers.image.licenses=Apache-2.0 \
      org.opencontainers.image.title=qs-console
WORKDIR /app
COPY --from=build /src/.output /app
ENV NODE_ENV=production \
    NITRO_HOST=0.0.0.0 \
    NITRO_PORT=3000
EXPOSE 3000
USER 65532:65532
CMD ["/app/server/index.mjs"]
