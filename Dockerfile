# syntax=docker/dockerfile:1.7
# LightMail — multi-stage build producing a minimal, non-root image.
# Multi-arch: docker buildx build --platform linux/amd64,linux/arm64 .

ARG GO_VERSION=1.27
ARG NODE_VERSION=24

# ---- 1. web client -------------------------------------------------------
FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-alpine AS web
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# ---- 2. Go binary (pure Go, cross-compiled, no CGO) ---------------------
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN rm -rf internal/web/dist
COPY --from=web /src/frontend/dist ./internal/web/dist
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X lightmail/internal/api.Version=${VERSION}" -o /out/lightmail ./cmd/lightmail \
 && mkdir -p /out/data

# ---- 3. runtime: distroless static (CA certificates, tzdata, no shell) ---
FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="LightMail" \
      org.opencontainers.image.description="Lightweight self-hosted multi-account mail client" \
      org.opencontainers.image.source="https://example.invalid/lightmail"
COPY --from=build /out/lightmail /usr/local/bin/lightmail
COPY --from=build --chown=65532:65532 /out/data /data
ENV LIGHTMAIL_DATA_DIR=/data \
    LIGHTMAIL_LISTEN=:8080
USER 65532:65532
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/lightmail", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/lightmail"]
CMD ["serve"]
