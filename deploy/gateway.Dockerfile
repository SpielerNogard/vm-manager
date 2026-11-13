# VMM_BIN=release (default) uses the binary of release VMM_VERSION, VMM_BIN=source builds it (make stack).
ARG VMM_BIN=release

FROM golang:1.27.1 AS bin-source
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-X vm-manager/internal/version.Version=$VERSION" -o /gateway ./cmd/gateway

# Release binaries are checked against the release's SHA256SUMS; curl -f fails on a missing version.
FROM alpine:3.22 AS bin-release
ARG VMM_VERSION
ARG VMM_RELEASE_URL
ARG TARGETARCH
RUN { [ -n "$VMM_VERSION" ] || { echo "VMM_VERSION is not set: put the release to run into deploy/.env" >&2; exit 1; }; } \
    && apk add --no-cache curl \
    && base=${VMM_RELEASE_URL:-https://github.com/SpielerNogard/vm-manager/releases/download/$VMM_VERSION} \
    && curl -fsSLO "$base/gateway-linux-$TARGETARCH" && curl -fsSLO "$base/SHA256SUMS" \
    && grep "  gateway-linux-$TARGETARCH\$" SHA256SUMS | sha256sum -c - \
    && install -m 0755 "gateway-linux-$TARGETARCH" /gateway

FROM bin-${VMM_BIN} AS bin

FROM ubuntu:24.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ovn-central openvswitch-switch ovn-host wireguard-tools iptables iproute2 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=bin /gateway /usr/local/bin/gateway
COPY deploy/gateway-start.sh /usr/local/bin/gateway-start.sh
CMD ["/usr/local/bin/gateway-start.sh"]
