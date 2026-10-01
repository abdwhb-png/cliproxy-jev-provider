PLUGIN_ID := cliproxy-jev-provider
OUTPUT := dist/$(PLUGIN_ID).so

.PHONY: fmt test transport race vet verify build

fmt:
	gofmt -w *.go

test:
	GOPROXY=off GOTOOLCHAIN=local go test -mod=readonly -short ./...

transport:
	GOPROXY=off GOTOOLCHAIN=local go test -mod=readonly -timeout 120s -run '^TestHostJEVTransport$$' -v .

race:
	GOPROXY=off GOTOOLCHAIN=local go test -mod=readonly -race -short ./...

vet:
	GOPROXY=off GOTOOLCHAIN=local go vet -mod=readonly ./...

verify: test race vet transport
	go mod verify

build:
	mkdir -p dist
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 GOPROXY=off GOTOOLCHAIN=local go build -mod=readonly -buildmode=c-shared -o $(OUTPUT) .
