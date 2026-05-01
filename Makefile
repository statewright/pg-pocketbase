.PHONY: setup test-pg test-sqlite test-all build clean

UPSTREAM_VERSION := $(shell cat UPSTREAM_VERSION)

setup:
	@if [ -d pocketbase ]; then echo "pocketbase/ already exists. Run 'make clean' first to re-setup."; exit 1; fi
	git clone --depth 1 --branch $(UPSTREAM_VERSION) https://github.com/pocketbase/pocketbase.git pocketbase
	rm -rf pocketbase/.git
	@echo "Removing files replaced by build-tag pairs..."
	rm -f pocketbase/core/db_table.go
	@echo "Applying _overlay files..."
	cp -r _overlay/* pocketbase/
	@echo "Applying patches..."
	@for p in $$(find patches -name '*.patch' -type f); do \
		target="pocketbase/$$(echo $$p | sed 's|^patches/||; s|\.patch$$||')"; \
		patch -p0 "$$target" < "$$p" || exit 1; \
	done
	@echo "Setup complete. PocketBase $(UPSTREAM_VERSION) with PostgreSQL _overlay."

test-pg:
	PG_TEST_URL="$${PG_TEST_URL:-postgres://pgpb:pgpb@localhost:5432?sslmode=disable}" \
		go test -tags postgres -count=1 -race ./pgpb/

test-sqlite:
	go test -count=1 -race ./pocketbase/...

test-all: test-pg test-sqlite

build:
	go build -tags "postgres,no_default_driver" -o pg-pocketbase ./examples/basic/

clean:
	rm -rf pocketbase/

sync-upstream:
	@echo "Current pinned version: $(UPSTREAM_VERSION)"
	@echo ""
	@echo "To update:"
	@echo "  1. Edit UPSTREAM_VERSION with the new tag"
	@echo "  2. make clean && make setup"
	@echo "  3. Verify patches applied cleanly"
	@echo "  4. make test-all"
	@echo ""
	@echo "If a patch fails, the upstream file changed. Manually resolve:"
	@echo "  1. Clone fresh upstream at new version"
	@echo "  2. Compare against _overlay/ and patches/"
	@echo "  3. Update patch files"
	@echo "  4. Re-run make setup && make test-all"
