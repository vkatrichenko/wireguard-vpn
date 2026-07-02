# Wireguard-vpn module Makefile

# Clean up
clean:
	@echo "Cleaning up..."
	@find . -type f -name "*.pyc" -delete
	@find . -type d -name "__pycache__" -exec rm -rf {} +
	@find . -type d -name "*.egg-info" -exec rm -rf {} +
	@rm -rf build/ dist/ .pytest_cache/
	@docker system prune -f || true


# Pre-commit
PRE_COMMIT_IMAGE=ghcr.io/antonbabenko/pre-commit-terraform:v1.105.0
SHELLCHECK_IMAGE=koalaman/shellcheck:v0.11.0

pre-commit:
	@echo "Running pre-commit (dockerized)..."
	@mkdir -p $$HOME/.cache/pre-commit-docker
	@docker run --rm \
	  -e "USERID=$$(id -u):$$(id -g)" \
	  -e "PRE_COMMIT_HOME=/pc-cache" \
	  -v "$$PWD:/lint" \
	  -v "$$HOME/.cache/pre-commit-docker:/pc-cache" \
	  -w /lint \
	  $(PRE_COMMIT_IMAGE) \
	  run -a


# Shellcheck — run separately from `pre-commit`; the dockerized pre-commit
# image (ghcr.io/antonbabenko/pre-commit-terraform) bundles no shellcheck and
# mounts no docker socket, so it can't host a shellcheck hook.
# scripts/wg-peer is deliberately extension-less (it's the installed CLI
# name) and is listed explicitly — a `scripts/*.sh` glob alone would skip it.
shellcheck:
	@echo "Running shellcheck on scripts/*.sh scripts/wg-peer..."
	@docker run --rm \
	  -v "$$PWD":/mnt -w /mnt \
	  --platform linux/amd64 \
	  $(SHELLCHECK_IMAGE) scripts/*.sh scripts/wg-peer


.PHONY: clean pre-commit shellcheck
