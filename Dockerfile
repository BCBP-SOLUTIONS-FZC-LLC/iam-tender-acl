# syntax=docker/dockerfile:1.7
#
# iam-tender-acl
#
# Multi-stage build producing a minimal, non-root, distroless runtime image
# for the Tender ACL service. The single binary runs both the HTTP server
# (TAC-1..4) and the tenant-lifecycle-tenderacl-q SQS consumer in one
# process via errgroup — there is no separate consumer binary.

########################################
# Stage: builder
########################################
# go.mod declares a `tool` dependency (golangci-lint/v2, see the `tool (...)`
# block and Makefile's `lint` target) — that directive requires Go >= 1.24 to
# even parse go.mod, so the builder must track go.mod's `go 1.26.5` line, not
# lag behind it. 1.26.6 additionally carries the go1.26.6 stdlib security
# fixes (crypto/tls, net/http, encoding/xml, encoding/asn1, html/template,
# net/url — see `make vuln-check`).
FROM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS builder

ARG BUILD_VERSION=dev
ARG SOURCE_DATE_EPOCH
ENV SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH}

WORKDIR /src

# Copy go.mod/go.sum first so dependency resolution is cached independently
# of application source changes.
COPY go.mod go.sum ./

# platform-gincommon, platform-pgcommon, and platform-events are private
# github.com/BCBP-SOLUTIONS-FZC-LLC modules (not vendored locally), fetched
# via git using a short-lived token — same secret-handling pattern as
# iam-org-membership/iam-catalog-admin's Dockerfiles.
# hadolint ignore=DL3008 — base image is digest-pinned; apt package versions
# track Bookworm security updates intentionally, not pegged to a point release.
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

ENV GOPRIVATE="github.com/BCBP-SOLUTIONS-FZC-LLC/*"
ENV GONOSUMDB="github.com/BCBP-SOLUTIONS-FZC-LLC/*"

RUN --mount=type=secret,id=go_private_token \
    TOKEN=$(cat /run/secrets/go_private_token 2>/dev/null || true) && \
    if [ -z "$TOKEN" ]; then echo "ERROR: go_private_token secret is missing or empty — pass --secret id=go_private_token,src=<token-file>"; exit 1; fi && \
    git config --global url."https://x-access-token:${TOKEN}@github.com/".insteadOf "https://github.com/" && \
    go mod download && \
    git config --global --unset url."https://x-access-token:${TOKEN}@github.com/".insteadOf

# Now copy the remainder of the source tree.
COPY . .

RUN CGO_ENABLED=0 GOOS=linux \
    GOPRIVATE="github.com/BCBP-SOLUTIONS-FZC-LLC/*" \
    go build \
    -trimpath \
    -ldflags="-s -w -X main.buildVersion=${BUILD_VERSION}" \
    -o /out/tender-acl \
    ./cmd/tender-acl

########################################
# Stage: runtime
########################################
# distroless/static-debian12:nonroot ships no shell, no package manager, and
# already includes a minimal set of CA certificates plus a preconfigured
# "nonroot" user/group (uid/gid 65532). This is sufficient for a statically
# linked (CGO_ENABLED=0) Go binary that only needs outbound TLS (to
# iam-org-membership's membershipcheck endpoint and to AWS SQS).
#
# If a fallback base image without bundled CA certs is ever substituted here
# (e.g. gcr.io/distroless/base-debian12 without the "-nonroot" cert bundle,
# or a scratch image), CA certificates would need to be copied explicitly:
#   COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
FROM gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a AS runtime

LABEL org.opencontainers.image.title="iam-tender-acl" \
      org.opencontainers.image.description="Tender ACL overlay service (tender_acl_entries) for the IAM stack — extracted from iam-org-membership per ADR-0007 Wave 3, interim pending Wave 4 merge into the Tender Service" \
      org.opencontainers.image.source="https://github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl" \
      org.opencontainers.image.vendor="BCBP Solutions" \
      org.opencontainers.image.licenses="Proprietary" \
      org.opencontainers.image.base.name="gcr.io/distroless/static-debian12:nonroot"

WORKDIR /

COPY --from=builder /out/tender-acl /tender-acl

USER nonroot:nonroot

EXPOSE 8080 9090

ENTRYPOINT ["/tender-acl"]
