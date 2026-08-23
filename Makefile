# Run `make check` before you push. It runs what CI runs, so a green check
# here means a green build there.

.DEFAULT_GOAL := help
.PHONY: help check lint fmt test readme static guards expansion fixture clean

export GOWORK = off

help:  ## show this
	@grep -hE '^[a-z-]+:.*?##' $(MAKEFILE_LIST) | sed 's/:.*##/\t/' | expand -t22

check: lint test static guards  ## everything CI runs, before you push

lint:  ## gofmt, vet, and staticcheck when installed
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "gofmt: the files above need formatting" && exit 1)
	go vet ./...
	@command -v staticcheck >/dev/null && staticcheck ./... || echo "staticcheck not installed, skipped"

fmt:  ## apply gofmt
	gofmt -w .

test:  ## the whole suite, including the example binary end to end
	go test ./...

readme:  ## just the README harness
	go test . -run 'Readme|GoBlock|ReferencedName|DocumentedProject|Skipped|DocumentedCommand' -v

# The builder compiles nodes exactly like this, a CGO dependency would
# break it. The test asserts the result is static.
static:  ## prove the example module compiles to a static linux binary
	go test . -run TestTheExampleCompilesToAStaticLinuxBinary -v

guards:  ## reintroduce each guarded bug and prove the suite goes red
	go run ./scripts/guards

expansion:  ## re-measure PARSE_EXPANSION after a toolchain upgrade
	go run ./scripts/measure-expansion

fixture:  ## regenerate the builder's acceptance fixture
	UPDATE_FIXTURES=1 go test . -run TestTheAcceptanceFixtureIsByteStable

clean:  ## remove test leftovers
	go clean -testcache
