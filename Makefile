.PHONY: build deps test race fuzz smoke fmt clean
GO ?= go

# Deliberate frontend dependency updates require updating frontend/README.md.
deps:
	test "$$(pkg-config --modversion sdl3)" = "3.4.18"
	test "$$(pkg-config --modversion sdl3-ttf)" = "3.2.2"

build: deps
	mkdir -p build/
	$(GO) build -o build/wolfnode ./cmd/wolfnode
	$(GO) build -o build/wolfclient ./cmd/wolfclient

clean:
	rm -rf build/*

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

smoke: build
	@state=$$(mktemp -d); trap 'rm -rf "$$state"' EXIT; SDL_VIDEODRIVER=dummy SDL_RENDER_DRIVER=software ./build/wolfclient -state-dir "$$state" -smoke-test

fmt:
	rg --files -g '*.go' | xargs gofmt -w
