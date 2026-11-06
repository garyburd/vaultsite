.PHONY: build install test vet

build:
	go build -o vaultsite .

install:
	go install .

test:
	go test ./...

vet:
	go vet ./...
