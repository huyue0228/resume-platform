PYTHON ?= python3
APP_VERSION ?= dev
IMAGE_PREFIX ?= resume-platform
PLATFORM ?= linux/amd64
COMPONENT ?= app
PUSH ?=
IMAGES_DIR ?= release/$(APP_VERSION)/images
.PHONY: check check-backend check-frontend check-release images image package
check: check-backend check-frontend check-release
check-backend:
	cd backend && $(PYTHON) -m resume_contracts.verify
	cd backend && $(PYTHON) manage.py check
	cd backend && $(PYTHON) manage.py makemigrations accounts core --check --dry-run
	cd backend && $(PYTHON) manage.py test apps.pipeline apps.ingestion apps.api apps.accounts
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
