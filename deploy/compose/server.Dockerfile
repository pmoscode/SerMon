# sermon-server built from the source of this repository (M2-26): web UI, then the static server
# binary with the UI embedded, then a distroless image without shell or package manager. Context is
# the repository root; server.Dockerfile.dockerignore lets in only web/, docs/help and server/.
# Every base is pinned by digest (scripts/check-compose.sh --hardened); there is deliberately no
# `# syntax=` line, which would pull an unpinned frontend image.
#
# Build args:
#   SERMON_RELEASE_KEY  public release key (scripts/release-sign.sh) that install.sh checks the
#                       agent binaries from /downloads against (ADR 0012); empty: no downloads
#   SERMON_VERSION      shown in the UI and in logs

FROM node:26.13.1-trixie-slim@sha256:e6bedef217677b276c591795e169840edd9096dbf13f29df8ef8d1856b8820bc AS web
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
# pnpm in the version of package.json's packageManager (Renovate keeps that one current, M2-39);
# pnpm-workspace.yaml keeps the supply-chain rules.
RUN npm install --global --no-fund --no-audit "pnpm@$(node -p 'require("./package.json").packageManager.split("@")[1]')"
RUN pnpm install --frozen-lockfile --ignore-scripts
COPY web/ ./
COPY docs/help/ /src/docs/help/
RUN pnpm exec svelte-kit sync && pnpm run i18n && pnpm run build && pnpm run check:size

FROM golang:1.27.4-trixie@sha256:f18c3d73a184fb0582828510722e723b63a93f7188d11dd223d9be6f6880e65d AS server
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
COPY --from=web /src/web/build/ ./web/dist/
ARG SERMON_VERSION=source
ARG SERMON_RELEASE_KEY=
RUN go build -trimpath -ldflags "-s -w -X main.version=${SERMON_VERSION} -X main.releaseKey=${SERMON_RELEASE_KEY}" \
		-o /out/sermon-server ./cmd/sermon-server \
	&& mkdir /out/state

# Static binary, CA certificates and time zones; runs as nonroot (65532). The state directory
# belongs to it, so the named volume inherits the ownership.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
COPY --from=server /out/sermon-server /usr/local/bin/sermon-server
COPY --from=server --chown=65532:65532 /out/state /var/lib/sermon
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/sermon-server"]
