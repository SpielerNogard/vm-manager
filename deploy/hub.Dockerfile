# VMM_BIN=release (default) uses the binary of release VMM_VERSION, VMM_BIN=source builds it (make stack).
ARG VMM_BIN=release

FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.27.1 AS bin-source
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist web/dist
RUN CGO_ENABLED=0 go build -ldflags "-X vm-manager/internal/version.Version=$VERSION" -o /hub ./cmd/hub

# Release binaries are checked against the release's SHA256SUMS; curl -f fails on a missing version.
FROM alpine:3.22 AS bin-release
ARG VMM_VERSION
ARG TARGETARCH
RUN apk add --no-cache curl && test -n "$VMM_VERSION" \
    && base=https://github.com/SpielerNogard/vm-manager/releases/download/$VMM_VERSION \
    && curl -fsSLO "$base/hub-linux-$TARGETARCH" && curl -fsSLO "$base/SHA256SUMS" \
    && grep "  hub-linux-$TARGETARCH\$" SHA256SUMS | sha256sum -c - \
    && install -m 0755 "hub-linux-$TARGETARCH" /hub

FROM bin-${VMM_BIN} AS bin

FROM gcr.io/distroless/static-debian13@sha256:448810357cc8e67b7208988abdba0f5ec3d3aebee0a5b13eb624979792cffc0f
COPY --from=bin /hub /usr/local/bin/hub
ENTRYPOINT ["hub"]
