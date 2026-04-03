APP=ccgo

.PHONY: test build run smoke

test:
	go test ./...

build:
	go build -o bin/$(APP) ./cmd/ccgo

run:
	go run ./cmd/ccgo run --provider mock "hello from ccgo"

smoke:
	go run ./cmd/ccgo print --provider mock "use echo from smoke"
