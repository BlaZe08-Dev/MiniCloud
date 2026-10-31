.PHONY: all build test test-race test-docs fmt vet run-coordinator run-worker demo clean db-up db-down db-psql db-reset-test test-db test-outage test-security

BIN_DIR := bin

all: fmt vet build test-race

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/coordinator ./cmd/coordinator
	go build -o $(BIN_DIR)/worker ./cmd/worker
	go build -o $(BIN_DIR)/mc ./cmd/mc
	go build -o $(BIN_DIR)/migrate ./cmd/migrate
	@echo "Build complete. Executables located in $(BIN_DIR)/"

test:
	go test -v ./...

test-race:
	go test -v -race ./...

test-docs:
	go test ./tests/docs

fmt:
	gofmt -s -w .

vet:
	go vet ./...

run-coordinator: build
	./$(BIN_DIR)/coordinator

run-worker: build
	./$(BIN_DIR)/worker

demo: build
	@echo "=== Starting MiniCloud Local Core Demo ==="
	@bash scripts/demo.sh

# Database provisioning targets (Slice 3b)
db-up:
	@docker compose -f deployments/docker-compose.yml --env-file .env up -d --wait
	@echo "PostgreSQL container is running and healthy on 127.0.0.1:5433"

db-down:
	@docker compose --env-file .env -f deployments/docker-compose.yml down
	@echo "PostgreSQL container stopped (named volumes preserved)"

db-psql:
	@docker compose -f deployments/docker-compose.yml --env-file .env exec -it postgres sh -c 'psql -U "$$POSTGRES_USER" -d "$$POSTGRES_DB"'

db-reset-test:
	@docker compose -f deployments/docker-compose.yml --env-file .env exec -T postgres sh -c '\
		TARGET="minicloud_test"; \
		case "$$TARGET" in *_test) ;; *) echo "Refusing to reset non-test DB $$TARGET" >&2; exit 1 ;; esac; \
		psql -U "$$POSTGRES_USER" -d postgres -c "DROP DATABASE IF EXISTS minicloud_test;" && \
		psql -U "$$POSTGRES_USER" -d postgres -c "CREATE DATABASE minicloud_test;" && \
		psql -U "$$POSTGRES_USER" -d postgres -c "GRANT ALL PRIVILEGES ON DATABASE minicloud_test TO \"$$POSTGRES_USER\";"'
	@echo "Test database minicloud_test reset successfully."

# Anchored integration test run by test-db. The -list check below fails the target when
# the pattern matches anything but exactly one test: go test -run exits 0 with
# "no tests to run" if the test is renamed, which would otherwise pass silently.
GRACE_TEST := ^TestPostgresWiring_CoordinatorRestartGrace_SurvivingCommit$$
GRACE_TEST_COUNT := 1

test-db:
	@if [ ! -f .env ]; then echo ".env file missing" >&2; exit 1; fi
	@set -e; \
	URL=$$(grep -E '^MINICLOUD_TEST_DATABASE_URL=' .env | cut -d= -f2-); \
	if [ -z "$$URL" ]; then echo "MINICLOUD_TEST_DATABASE_URL not set in .env" >&2; exit 1; fi; \
	N=$$(env -u MINICLOUD_TEST_DATABASE_URL go test -list '$(GRACE_TEST)' ./tests/integration/ | grep -c '^Test' || true); \
	if [ "$$N" != "$(GRACE_TEST_COUNT)" ]; then echo "FALSE-GREEN GUARD: pattern '$(GRACE_TEST)' lists $$N tests in ./tests/integration/, expected $(GRACE_TEST_COUNT); was the test renamed?" >&2; exit 1; fi; \
	MINICLOUD_TEST_DATABASE_URL="$$URL" go test -v -race -count=1 ./internal/storage/postgres/... ./internal/coordinator/...; \
	MINICLOUD_TEST_DATABASE_URL="$$URL" go test -v -race -count=1 -run '$(GRACE_TEST)' ./tests/integration/

# P4-4e executed fault-injection and API-abuse security suite, against the .env test database only.
# The fault proxy simulates every outage; this target never touches the shared container and never
# sets the container-control variables. The target requires a dedicated per-agent test database
# (refusing minicloud_test early); override with ALLOW_SHARED_DB=1.
# The sorted names printed by go test -list for the pattern
# must equal tests/security-tests.list exactly: a count alone would stay green when one test is
# renamed and another added, and go test -run exits 0 for a pattern that matches nothing. On a
# difference the target prints the missing and the extra names and fails. After adding, removing or
# renaming a security test, regenerate the list with:
#   env -u MINICLOUD_TEST_DATABASE_URL go test -list '^(TestFault_|TestAbuse_)' ./tests/faultinject/ ./tests/abuse/ | grep '^Test' | LC_ALL=C sort > tests/security-tests.list
SECURITY_TESTS := ^(TestFault_|TestAbuse_)
SECURITY_TEST_LIST := tests/security-tests.list
ALLOW_SHARED_DB ?=
MINICLOUD_TEST_DATABASE_URL ?=

test-security:
	@if [ ! -f $(SECURITY_TEST_LIST) ]; then echo "$(SECURITY_TEST_LIST) missing" >&2; exit 1; fi
	@set -e; \
	if [ -n "$${MINICLOUD_TEST_DATABASE_URL+x}" ]; then \
		URL="$$MINICLOUD_TEST_DATABASE_URL"; \
	elif [ -n "$(MINICLOUD_TEST_DATABASE_URL)" ]; then \
		URL="$(MINICLOUD_TEST_DATABASE_URL)"; \
	elif [ -f .env ]; then \
		URL=$$(grep -E '^MINICLOUD_TEST_DATABASE_URL=' .env | cut -d= -f2-); \
	else \
		URL=""; \
	fi; \
	ALLOW_SHARED_DB="$${ALLOW_SHARED_DB:-$(ALLOW_SHARED_DB)}" tests/require-own-db.sh "$$URL"; \
	ACT=$$(mktemp); trap 'rm -f "$$ACT"' EXIT; \
	env -u MINICLOUD_TEST_DATABASE_URL go test -list '$(SECURITY_TESTS)' ./tests/faultinject/ ./tests/abuse/ | grep '^Test' | LC_ALL=C sort > "$$ACT" || true; \
	if ! diff -q $(SECURITY_TEST_LIST) "$$ACT" >/dev/null; then \
		echo "FALSE-GREEN GUARD: pattern '$(SECURITY_TESTS)' does not match $(SECURITY_TEST_LIST)" >&2; \
		echo "missing (in the list, not found by go test -list):" >&2; LC_ALL=C comm -23 $(SECURITY_TEST_LIST) "$$ACT" | sed 's/^/  /' >&2; \
		echo "extra (found by go test -list, not in the list):" >&2; LC_ALL=C comm -13 $(SECURITY_TEST_LIST) "$$ACT" | sed 's/^/  /' >&2; \
		exit 1; \
	fi; \
	MINICLOUD_TEST_DATABASE_URL="$$URL" go test -v -race -count=1 -timeout 20m -run '$(SECURITY_TESTS)' ./tests/faultinject/ ./tests/abuse/

# DANGEROUS: runs the tests that stop and start the SHARED minicloud-postgres container.
# Never part of test-db or any default target. Requires CONFIRM=minicloud-postgres.
OUTAGE_TESTS := ^(TestPostgresWiring_DatabaseOutageAndRecovery|TestSecurity_PartD_Outage_MidDispatchAndMidCommit|TestSecurity_PartD_LeakScan)$$
OUTAGE_TEST_COUNT := 3

test-outage:
	@printf '\033[1m\033[31mWARNING: test-outage STOPS AND STARTS THE SHARED minicloud-postgres CONTAINER.\033[0m\n' >&2
	@printf '\033[1mEvery agent and worktree using 127.0.0.1:5433 loses its database during the run.\033[0m\n' >&2
	@if [ "$(CONFIRM)" != "minicloud-postgres" ]; then echo "REFUSED: re-run as: make test-outage CONFIRM=minicloud-postgres" >&2; exit 1; fi
	@if [ ! -f .env ]; then echo ".env file missing" >&2; exit 1; fi
	@set -e; \
	URL=$$(grep -E '^MINICLOUD_TEST_DATABASE_URL=' .env | cut -d= -f2-); \
	if [ -z "$$URL" ]; then echo "MINICLOUD_TEST_DATABASE_URL not set in .env" >&2; exit 1; fi; \
	N=$$(env -u MINICLOUD_TEST_DATABASE_URL go test -list '$(OUTAGE_TESTS)' ./tests/integration/ | grep -c '^Test' || true); \
	if [ "$$N" != "$(OUTAGE_TEST_COUNT)" ]; then echo "FALSE-GREEN GUARD: outage pattern lists $$N tests, expected $(OUTAGE_TEST_COUNT)" >&2; exit 1; fi; \
	MINICLOUD_TEST_DATABASE_URL="$$URL" MINICLOUD_ALLOW_CONTAINER_RESTART=1 MINICLOUD_CONFIRM_CONTAINER=minicloud-postgres \
		go test -v -race -count=1 -run '$(OUTAGE_TESTS)' ./tests/integration/

clean:
	rm -rf $(BIN_DIR)
