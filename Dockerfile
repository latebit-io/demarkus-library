# demarkus-library image. Single binary; templates and static assets are
# embedded (go:embed), health probe is HTTP GET /health, so nothing else
# is bundled.
#
# Pre-built binaries are staged into dist/docker/<arch>/ by the release
# workflow. The Dockerfile is COPY-only — no `RUN` step — so a buildx
# multi-arch build under QEMU completes in seconds instead of minutes
# (no Go compile under emulation).
#
# Base is distroless/static:nonroot — it ships
# /etc/ssl/certs/ca-certificates.crt for the library's outbound HTTPS to
# the broker (OAuth token exchange + MCP gateway reads), and runs as UID
# 65532 by default. Defense-in-depth USER directive matches the chart's
# podSecurityContext.runAsUser.
#
# Build context = repo root. Staged paths the Dockerfile expects:
#   dist/docker/amd64/demarkus-library
#   dist/docker/arm64/demarkus-library
#   dist/docker/armv7/demarkus-library

FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETARCH
ARG TARGETVARIANT
COPY dist/docker/${TARGETARCH}${TARGETVARIANT}/demarkus-library /demarkus-library
USER 65532:65532
EXPOSE 8080/tcp
ENTRYPOINT ["/demarkus-library"]
