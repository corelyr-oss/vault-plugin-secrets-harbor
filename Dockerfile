# Image for Vault's containerized plugin runtime. goreleaser (dockers_v2) copies
# the pre-built, statically linked binary for each platform in; the image
# contains nothing else and runs as non-root.
FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/vault-plugin-secrets-harbor /bin/vault-plugin-secrets-harbor
USER nonroot:nonroot
ENTRYPOINT ["/bin/vault-plugin-secrets-harbor"]
