.PHONY: web hub node node-linux storage test stack

web:
	npm --prefix web ci
	npm --prefix web run build
	touch web/dist/.gitkeep

hub:
	go build -o bin/hub ./cmd/hub

storage:
	go build -o bin/storage ./cmd/storage

node:
	go build -o bin/node ./cmd/node
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/netagent-linux-arm64 ./cmd/netagent
	codesign --force --entitlements node.entitlements -s - bin/node

node-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/node-linux-amd64 ./cmd/node

test:
	go test ./...

stack:
	docker compose -f deploy/compose.yaml up -d --build
