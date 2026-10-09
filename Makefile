# Require the embedded AVIF decoder; host libraries could change variant output.
TAGS := nodynamic

.PHONY: build install test vet

build:
	go build -tags $(TAGS) -o vaultsite .

install:
	go install -tags $(TAGS) .

test:
	go test -tags $(TAGS) ./...

vet:
	go vet -tags $(TAGS) ./...
