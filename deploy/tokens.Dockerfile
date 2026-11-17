# VMM_BIN=release (default) uses the binary of release VMM_VERSION, VMM_BIN=source builds it (make stack).
ARG VMM_BIN=release

FROM golang:1.27.1 AS bin-source
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-X vm-manager/internal/version.Version=$VERSION" -o /tokens ./cmd/tokens

# Release binaries are checked against the release's SHA256SUMS; curl -f fails on a missing version.
FROM alpine:3.22 AS bin-release
ARG VMM_VERSION
ARG VMM_RELEASE_URL
ARG TARGETARCH
RUN { [ -n "$VMM_VERSION" ] || { echo "VMM_VERSION is not set: put the release to run into deploy/.env" >&2; exit 1; }; } \
    && apk add --no-cache curl \
    && base=${VMM_RELEASE_URL:-https://github.com/SpielerNogard/vm-manager/releases/download/$VMM_VERSION} \
    && curl -fsSLO "$base/tokens-linux-$TARGETARCH" && curl -fsSLO "$base/SHA256SUMS" \
    && grep "  tokens-linux-$TARGETARCH\$" SHA256SUMS | sha256sum -c - \
    && install -m 0755 "tokens-linux-$TARGETARCH" /tokens

FROM bin-${VMM_BIN} AS bin

FROM gcr.io/distroless/static-debian12
COPY --from=bin /tokens /tokens
ENTRYPOINT ["/tokens"]
