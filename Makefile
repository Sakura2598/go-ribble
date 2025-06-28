.PHONY: geth all test lint fmt clean devtools help cross-all

GOBIN = ./build/bin
GORUN = go run
VERSION ?= v1.0.0

#? geth: Build geth.
geth:
	$(GORUN) build/ci.go install ./cmd/geth
	@echo "Done building."
	@echo "Run \"$(GOBIN)/geth\" to launch geth."

#? all: Build all packages and executables.
all:
	$(GORUN) build/ci.go install

#? test: Run the tests.
test: all
	$(GORUN) build/ci.go test

#? lint: Run certain pre-selected linters.
lint: ## Run linters.
	$(GORUN) build/ci.go lint

#? fmt: Ensure consistent code formatting.
fmt:
	gofmt -s -w $(shell find . -name "*.go")

#? clean: Clean go cache, built executables, and the auto generated folder.
clean:
	go clean -cache
	rm -fr build/_workspace/pkg/ $(GOBIN)/* builds/$(VERSION)

#? devtools: Install recommended developer tools.
devtools:
	env GOBIN= go install golang.org/x/tools/cmd/stringer@latest
	env GOBIN= go install github.com/fjl/gencodec@latest
	env GOBIN= go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	env GOBIN= go install ./cmd/abigen
	@type "solc" 2> /dev/null || echo 'Please install solc'
	@type "protoc" 2> /dev/null || echo 'Please install protoc'

#? help: Get more info on make commands.
help: Makefile
	@echo ''
	@echo 'Usage:'
	@echo '  make [target]'
	@echo ''
	@echo 'Targets:'
	@sed -n 's/^#?//p' $< | column -t -s ':' |  sort | sed -e 's/^/ /'

#? cross-all: Cross compile geth for linux, windows, darwin (amd64).
#? cross-all: Cross build all tools for multiple OS/ARCH.
cross-all:
	@echo "🔧 Building all tools for multiple platforms"
	@VERSION=$(VERSION); \
	COMMIT=$$(git rev-parse --short HEAD); \
	DATE=$$(date -u +%Y%m%d); \
	TARGETS="linux/amd64 windows/amd64 darwin/amd64"; \
	TOOLS="geth abigen bootnode clef evm puppeth rlpdump"; \
	for target in $$TARGETS; do \
		OS=$${target%%/*}; ARCH=$${target##*/}; \
		for tool in $$TOOLS; do \
			EXT=""; [ "$$OS" = "windows" ] && EXT=".exe"; \
			OUTDIR=outs/$(VERSION)/$$OS-$$ARCH; \
			mkdir -p $$OUTDIR; \
			echo "👉 Building $$tool for $$OS/$$ARCH → $$OUTDIR/$$tool$$EXT"; \
			GOOS=$$OS GOARCH=$$ARCH CGO_ENABLED=0 go build \
				-o $$OUTDIR/$$tool$$EXT \
				-ldflags "-buildid=none -X github.com/Sakura2598/go-ribble/internal/version.gitCommit=$$COMMIT -X github.com/Sakura2598/go-ribble/internal/version.gitDate=$$DATE" \
				-tags urfave_cli_no_docs,ckzg \
				-trimpath ./cmd/$$tool || echo "⚠️ Failed to build $$tool for $$OS/$$ARCH"; \
		done; \
	done
	@echo "✅ Cross build completed → builds/$(VERSION)/*"