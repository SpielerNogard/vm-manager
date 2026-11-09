.PHONY: web hub node test

web:
	npm --prefix web ci
	npm --prefix web run build
	touch web/dist/.gitkeep

hub:
	go build -o bin/hub ./cmd/hub

test:
	go test ./...
