.PHONY: web hub node test

web:
	npm --prefix web ci
	npm --prefix web run build
	touch web/dist/.gitkeep

hub:
	go build -o bin/hub ./cmd/hub

node:
	go build -o bin/node ./cmd/node
	codesign --force --entitlements node.entitlements -s - bin/node

test:
	go test ./...
