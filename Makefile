# Platforms that both Docker and Go support. This is the single list used for
# local builds, CI and releases; add an architecture here to support it.
PLATFORMS := linux/amd64 linux/386 linux/arm64 linux/arm/v7 linux/arm/v6 \
	linux/arm/v5 linux/riscv64 linux/ppc64le linux/s390x linux/mips64le linux/loong64

# The host's Docker platform, from `uname -m`. Override with PLATFORM=...
ARCH_x86_64      := linux/amd64
ARCH_amd64       := linux/amd64
ARCH_i386        := linux/386
ARCH_i686        := linux/386
ARCH_aarch64     := linux/arm64
ARCH_arm64       := linux/arm64
ARCH_armv7l      := linux/arm/v7
ARCH_armv6l      := linux/arm/v6
ARCH_armv5tel    := linux/arm/v5
ARCH_armv5tejl   := linux/arm/v5
ARCH_riscv64     := linux/riscv64
ARCH_ppc64le     := linux/ppc64le
ARCH_s390x       := linux/s390x
ARCH_mips64      := linux/mips64le
ARCH_loongarch64 := linux/loong64
PLATFORM ?= $(ARCH_$(shell uname -m))

IMAGE   := cloudflare-ddns
BINARY  := cloudflare-ddns
COMPOSE := DOCKER_DEFAULT_PLATFORM=$(PLATFORM) docker compose

.PHONY: help build test lint vuln check check-platforms image images run stop logs release clean platform

help: ## List the targets
	@grep -E '^[a-z]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

platform: ## Print the detected Docker platform
	@$(if $(PLATFORM),echo $(PLATFORM),$(error unsupported architecture "$(shell uname -m)"; set PLATFORM to one of: $(PLATFORMS)))

build: ## Build the binary for this machine
	go build -trimpath -o $(BINARY) ./cmd/cloudflare-ddns

test: ## Run the tests with the race detector
	go test -race ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

vuln: ## Check for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

check: test lint vuln check-platforms ## Run every check CI runs

check-platforms: ## Check .goreleaser.yaml builds every platform in PLATFORMS
	@want="$$(echo $(PLATFORMS) | tr ' ' '\n' | sed -E 's#/v([0-9])$$#_\1#; s#/#_#g' | sort)"; \
		have="$$(grep -oE 'linux_[a-z0-9_]+' .goreleaser.yaml | sort)"; \
		[ "$$want" = "$$have" ] || { printf 'PLATFORMS:\n%s\n.goreleaser.yaml:\n%s\n' "$$want" "$$have"; exit 1; }

image: platform ## Build the image for this machine's platform
	docker buildx build --platform $(PLATFORM) --target final --load -t $(IMAGE) .

images: ## Build the image for every supported platform
	docker buildx build --platform $(subst $(eval ) ,$(comma),$(PLATFORMS)) --target final .

run: platform ## Build and start the daemon with Docker Compose
	$(COMPOSE) up -d --build

stop: ## Stop the daemon
	docker compose down

logs: ## Follow the daemon's logs
	docker compose logs -f

release: ## Tag a release locally: make release VERSION=v1.2.3 (does not push)
	@echo "$(VERSION)" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$$' \
		|| { echo "VERSION must be semver with a v prefix, such as v1.2.3 or v1.3.0-rc.1"; exit 1; }
	@latest=$$(git tag --list 'v*' --sort=-version:refname | head -1); \
		if [ -n "$$latest" ] && [ "$$(printf '%s\n%s\n' "$$latest" "$(VERSION)" | sort -V | tail -1)" != "$(VERSION)" -o "$$latest" = "$(VERSION)" ]; then \
			echo "$(VERSION) must be newer than $$latest"; exit 1; fi
	@test -z "$$(git status --porcelain)" || { echo "commit or stash changes first"; exit 1; }
	git tag -a $(VERSION) -m "$(VERSION)"
	@echo "Tagged $(VERSION). Push it with: git push origin $(VERSION)"

clean: ## Remove the built binary
	rm -rf $(BINARY) dist

comma := ,
