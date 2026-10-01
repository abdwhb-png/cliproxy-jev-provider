PLUGIN_ID := cliproxy-jev-provider
OUTPUT := dist/$(PLUGIN_ID).so

.PHONY: fmt test transport browser race vet verify build

fmt:
	gofmt -w *.go

test:
	GOPROXY=off GOTOOLCHAIN=local go test -mod=readonly -short ./...

transport:
	GOPROXY=off GOTOOLCHAIN=local go test -mod=readonly -count=1 -timeout 180s -run '^TestHostJEVTransport$$' -v .

browser:
	JEV_TEST_BROWSER=1 $(MAKE) transport

race:
	GOPROXY=off GOTOOLCHAIN=local go test -mod=readonly -race -short ./...

vet:
	GOPROXY=off GOTOOLCHAIN=local go vet -mod=readonly ./...

verify: test race vet browser
	go mod verify

build:
	mkdir -p $(dir $(OUTPUT))
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 GOPROXY=off GOTOOLCHAIN=local go build -mod=readonly -buildmode=c-shared -o $(OUTPUT) .
