VERSION ?= dev
LDFLAGS = -X vm-manager/internal/version.Version=$(VERSION)

.PHONY: web hub node node-linux storage test stack dist dist-darwin checksums

web:
	npm --prefix web ci
	npm --prefix web run build
	touch web/dist/.gitkeep

hub:
	go build -ldflags "$(LDFLAGS)" -o bin/hub ./cmd/hub

storage:
	go build -ldflags "$(LDFLAGS)" -o bin/storage ./cmd/storage

node:
	go build -ldflags "$(LDFLAGS)" -o bin/node ./cmd/node
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/netagent-linux-arm64 ./cmd/netagent
	codesign --force --entitlements node.entitlements -s - bin/node

node-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/node-linux-amd64 ./cmd/node

# Release binaries as cmd/os/arch; the hub embeds web/dist, so run `make web` first.
DIST_LINUX = hub/linux/amd64 hub/linux/arm64 gateway/linux/amd64 gateway/linux/arm64 \
	storage/linux/amd64 storage/linux/arm64 node/linux/amd64 netagent/linux/arm64

dist:
	mkdir -p dist
	for t in $(DIST_LINUX); do \
		set -- $$(echo $$t | tr / ' '); \
		CGO_ENABLED=0 GOOS=$$2 GOARCH=$$3 go build -ldflags "$(LDFLAGS)" -o dist/$$1-$$2-$$3 ./cmd/$$1 || exit 1; \
	done

dist-darwin:
	mkdir -p dist
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/node-darwin-arm64 ./cmd/node
	codesign --force --entitlements node.entitlements -s - dist/node-darwin-arm64

checksums:
	cd dist && rm -f SHA256SUMS && shasum -a 256 * > SHA256SUMS

test:
	go test ./...

stack:
	docker compose -f deploy/compose.yaml up -d --build
