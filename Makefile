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
	go build -ldflags "$(LDFLAGS)" -o bin/vm-manager-node ./cmd/node
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/netagent-linux-arm64 ./cmd/netagent
	codesign --force --entitlements node.entitlements -s - -i dev.vm-manager.node bin/vm-manager-node

node-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/vm-manager-node-linux-amd64 ./cmd/node

# Release binaries as cmd/os/arch; the hub embeds web/dist, so run `make web` first.
DIST_LINUX = hub/linux/amd64 hub/linux/arm64 gateway/linux/amd64 gateway/linux/arm64 \
	storage/linux/amd64 storage/linux/arm64 node/linux/amd64 netagent/linux/arm64

dist:
	mkdir -p dist
	for t in $(DIST_LINUX); do \
		set -- $$(echo $$t | tr / ' '); \
		case $$1 in hub|node) out=vm-manager-$$1 ;; *) out=$$1 ;; esac; \
		CGO_ENABLED=0 GOOS=$$2 GOARCH=$$3 go build -ldflags "$(LDFLAGS)" -o dist/$$out-$$2-$$3 ./cmd/$$1 || exit 1; \
	done

dist-darwin:
	mkdir -p dist
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/vm-manager-node-darwin-arm64 ./cmd/node
	codesign --force --entitlements node.entitlements -s - -i dev.vm-manager.node dist/vm-manager-node-darwin-arm64

checksums:
	cd dist && rm -f SHA256SUMS && shasum -a 256 * > SHA256SUMS

test:
	go test ./...

stack:
	VMM_BIN=source VMM_VERSION=dev docker compose -f deploy/compose.yaml up -d --build
