.PHONY: build deps node test race fuzz smoke fmt clean browser-test browser-smoke browser-native
GO ?= go
BROWSER ?= /opt/brave-bin/brave

# Deliberate frontend dependency updates require updating frontend/README.md.
deps:
	test "$$(pkg-config --modversion sdl3)" = "3.4.18"
	test "$$(pkg-config --modversion sdl3-ttf)" = "3.2.2"

build: deps
	mkdir -p build/
	$(GO) build -o build/wolfnode ./cmd/wolfnode
	$(GO) build -o build/wolfclient ./cmd/wolfclient

node:
	mkdir -p build/
	CGO_ENABLED=0 $(GO) build -o build/wolfnode ./cmd/wolfnode

clean:
	rm -f build/wolfclient build/wolfnode

test:
	$(GO) test ./...
	$(GO) vet ./...

race:
	$(GO) test -race ./...

fuzz:
	$(GO) test ./internal/protocol -run '^$$' -fuzz '^FuzzFrameV2$$' -fuzztime 10s -parallel 2
	$(GO) test ./internal/protocol -run '^$$' -fuzz '^FuzzAuthPayload$$' -fuzztime 10s -parallel 2
	$(GO) test ./internal/fetch -run '^$$' -fuzz '^FuzzURLAndRedirectValidation$$' -fuzztime 10s -parallel 2
	$(GO) test ./internal/enrolment -run '^$$' -fuzz '^FuzzInvitation$$' -fuzztime 10s -parallel 2
	$(GO) test ./internal/protocol/v3 -run '^$$' -fuzz '^FuzzV3Frame$$' -fuzztime 10s -parallel 2
	$(GO) test ./internal/protocol/v3 -run '^$$' -fuzz '^FuzzV3Control$$' -fuzztime 10s -parallel 2
	$(GO) test ./internal/pixelstream -run '^$$' -fuzz '^FuzzPixelUpdate$$' -fuzztime 10s -parallel 2

smoke: build
	@state=$$(mktemp -d); trap 'rm -rf "$$state"' EXIT; SDL_VIDEODRIVER=dummy SDL_RENDER_DRIVER=software ./build/wolfclient -state-dir "$$state" -smoke-test

fmt:
	rg --files -g '*.go' | xargs gofmt -w

browser-test: build
	@test -x "$(BROWSER)" || (echo 'Set BROWSER to a maintained Chromium executable'; exit 1)
	WOLF_BROWSER_BIN="$(BROWSER)" WOLF_CLIENT_BIN="$(CURDIR)/build/wolfclient" WOLF_ARTIFACT_DIR="$(CURDIR)/build/v0.5" $(GO) test ./internal/browser ./internal/session -count=1 -v -timeout 120s

browser-native: build
	WOLF_BROWSER_BIN="$(BROWSER)" WOLF_CLIENT_BIN="$(CURDIR)/build/wolfclient" WOLF_NATIVE_DISPLAY=1 WOLF_ARTIFACT_DIR="$(CURDIR)/build/v0.5" $(GO) test ./internal/session -run TestNativeClientBrowserScreenshot -count=1 -v -timeout 50s

browser-smoke: browser-test
	python3 tools/smoke_v05.py --browser "$(BROWSER)"
