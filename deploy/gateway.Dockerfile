FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /gateway ./cmd/gateway

FROM ubuntu:24.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ovn-central openvswitch-switch ovn-host wireguard-tools iptables iproute2 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /gateway /usr/local/bin/gateway
COPY deploy/gateway-start.sh /usr/local/bin/gateway-start.sh
CMD ["/usr/local/bin/gateway-start.sh"]
