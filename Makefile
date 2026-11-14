.PHONY: web web-test

# Builds the management page and puts it where internal/web embeds it. The
# shared part comes from cpa-provider-core (scripts/sync-core.mjs copies it to
# web/src/core; set GOWORK or CPA_PROVIDER_CORE_DIR for a local checkout).
web:
	cd web && node scripts/sync-core.mjs && npm ci && npm run build
	mkdir -p internal/web/dist
	cp web/dist/index.html internal/web/dist/index.html

web-test:
	cd web && node scripts/sync-core.mjs && npm test
