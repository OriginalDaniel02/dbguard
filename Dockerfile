# The image is assembled in CI from the already-built, already-tested static binaries
# (dist/dbguard_linux_amd64, dist/dbguard_linux_arm64): nothing is compiled here.
#
#   docker run --rm ghcr.io/originaldaniel02/dbguard version
#   docker run --rm -v "$PWD:/work:ro" -w /work ghcr.io/originaldaniel02/dbguard check db/migration
#
# distroless/static carries CA certificates (for TLS database connections and Slack webhooks)
# and runs as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETARCH
LABEL org.opencontainers.image.title="dbguard" \
      org.opencontainers.image.description="Catch database migrations that will lock production, and schema drift" \
      org.opencontainers.image.source="https://github.com/OriginalDaniel02/dbguard" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY dist/dbguard_linux_${TARGETARCH} /usr/local/bin/dbguard
ENTRYPOINT ["/usr/local/bin/dbguard"]
