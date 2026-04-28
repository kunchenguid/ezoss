GO ?= go
GOFMT ?= gofmt
BINARY ?= ezoss
HOST_BINARY := $(BINARY)$(if $(filter Windows_NT,$(OS)),.exe,)
CMD_DIR := ./cmd/ezoss
DIST_DIR ?= ./dist
VERSION ?= dev
# UMAMI_WEBSITE_ID is the Umami site UUID baked into release builds. When set,
# the binary emits telemetry to UMAMI_HOST (or cloud.umami.is by default).
# Leave unset for local builds; users can also override at runtime via the
# EZOSS_UMAMI_WEBSITE_ID env var, which takes precedence over the build-time
# default.
UMAMI_WEBSITE_ID ?=
LDFLAGS := -X github.com/kunchenguid/ezoss/internal/buildinfo.Version=$(VERSION) \
           -X github.com/kunchenguid/ezoss/internal/buildinfo.TelemetryWebsiteID=$(UMAMI_WEBSITE_ID)
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

.PHONY: build dist demo docs-build install test lint fmt fmt-check

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o ./bin/$(HOST_BINARY) $(CMD_DIR)

dist: build
	@rm -rf $(DIST_DIR)
	@mkdir -p $(DIST_DIR)
	@set -e; \
	for platform in $(PLATFORMS); do \
		goos=$${platform%/*}; \
		goarch=$${platform#*/}; \
		stage_name="$(BINARY)-$(VERSION)-$${goos}-$${goarch}"; \
		stage_path="$(DIST_DIR)/$$stage_name"; \
		binary_name="$(BINARY)"; \
		if [ "$$goos" = "windows" ]; then binary_name="$(BINARY).exe"; fi; \
		rm -rf "$$stage_path"; \
		mkdir -p "$$stage_path"; \
		GOOS=$$goos GOARCH=$$goarch $(GO) build -ldflags "$(LDFLAGS)" -o "$$stage_path/$$binary_name" $(CMD_DIR); \
		cp LICENSE README.md "$$stage_path/"; \
		if [ "$$goos" = "windows" ]; then \
			$(GO) run ./internal/releasecmd archive -format zip -source "$$stage_path" -output "$(DIST_DIR)/$$stage_name.zip"; \
		else \
			$(GO) run ./internal/releasecmd archive -format tar.gz -source "$$stage_path" -output "$(DIST_DIR)/$$stage_name.tar.gz"; \
		fi; \
		rm -rf "$$stage_path"; \
	done
	@set -e; \
	checksum_cmd=""; \
	if command -v shasum >/dev/null 2>&1; then \
		checksum_cmd="shasum -a 256"; \
	elif command -v sha256sum >/dev/null 2>&1; then \
		checksum_cmd="sha256sum"; \
	else \
		echo "error: required checksum tool not found (need shasum or sha256sum)" >&2; \
		exit 1; \
	fi; \
	cd $(DIST_DIR) && $$checksum_cmd *.tar.gz *.zip > checksums.txt

demo: build
	vhs demo.tape
	ffmpeg -y -i demo_raw.gif -filter_complex \
	  "[0:v]setpts=PTS/1.6,fps=18,scale=1100:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=128:stats_mode=diff[p];[b][p]paletteuse=dither=sierra2_4a:diff_mode=rectangle" \
	  demo.gif
	rm -f demo_raw.gif

docs-build:
	npm --prefix ./docs ci
	npm --prefix ./docs run build

install: build
	$(GO) install -ldflags "$(LDFLAGS)" $(CMD_DIR)
	@if [ "$${EZOSS_SKIP_DAEMON:-}" != "1" ]; then \
		install_bin="$$($(GO) env GOBIN)"; \
		if [ -z "$$install_bin" ]; then install_bin="$$($(GO) env GOPATH)/bin"; fi; \
		"$$install_bin/$(HOST_BINARY)" daemon install; \
		"$$install_bin/$(HOST_BINARY)" daemon restart; \
	fi

test:
	$(GO) test ./...

lint:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

fmt-check:
	@test -z "$$($(GOFMT) -l $$(git ls-files '*.go'))"
