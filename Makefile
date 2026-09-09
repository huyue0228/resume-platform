PYTHON ?= python3
APP_VERSION ?= dev
IMAGE_PREFIX ?= resume-platform
PLATFORM ?= linux/amd64
COMPONENT ?= app
PUSH ?=
IMAGES_DIR ?= release/$(APP_VERSION)/images
.PHONY: check check-backend check-frontend check-release build frontend-assets images image package
check: check-backend check-frontend check-release
check-backend:
	go test -race ./...
	go vet ./...
	go build -o dist/resume-platform ./cmd/resume-platform
build: frontend-assets
	mkdir -p dist
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/resume-platform ./cmd/resume-platform
frontend-assets:
	cd frontend && npm run build
	find internal/web/assets -mindepth 1 ! -name .keep -delete
	cp -R frontend/dist/. internal/web/assets/
check-frontend:
	cd frontend && npm run lint && npm test && npm run build
check-release:
	$(PYTHON) -m unittest discover -s tools/tests
images:
	$(PYTHON) tools/release.py images --version "$(APP_VERSION)" --image-prefix "$(IMAGE_PREFIX)" --platform "$(PLATFORM)" $(PUSH)
image:
	$(PYTHON) tools/release.py images --component "$(COMPONENT)" --version "$(APP_VERSION)" --image-prefix "$(IMAGE_PREFIX)" --platform "$(PLATFORM)" $(PUSH)
package:
	$(PYTHON) tools/release.py package --version "$(APP_VERSION)" --images-dir "$(IMAGES_DIR)"
