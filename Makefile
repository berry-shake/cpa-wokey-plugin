.PHONY: web web-test

# Builds the management page and puts it where internal/web embeds it.
web:
	cd web && npm ci && npm run build
	mkdir -p internal/web/dist
	cp web/dist/index.html internal/web/dist/index.html

web-test:
	cd web && npm test
