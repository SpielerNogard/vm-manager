FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /storage ./cmd/storage

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends nbdkit && rm -rf /var/lib/apt/lists/*
COPY --from=build /storage /usr/local/bin/storage
VOLUME /disks
EXPOSE 10809
ENTRYPOINT ["storage"]
