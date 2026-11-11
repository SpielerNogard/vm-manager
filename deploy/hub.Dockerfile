FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist web/dist
RUN CGO_ENABLED=0 go build -o /hub ./cmd/hub

FROM gcr.io/distroless/static
COPY --from=build /hub /usr/local/bin/hub
ENTRYPOINT ["hub"]
