#!/bin/sh
set -e

# Determine raw URL: argument takes precedence over environment variable.
if [ $# -ge 1 ]; then
	RAW_URL="$1"
else
	RAW_URL="${MINICLOUD_TEST_DATABASE_URL:-}"
fi

# Extract database name handling ports, query strings, and optional scheme.
NO_QUERY="${RAW_URL%%\?*}"
if [ "$NO_QUERY" != "${NO_QUERY#*://}" ]; then
	REST="${NO_QUERY#*://}"
	if [ "$REST" != "${REST#*/}" ]; then
		PATH_PART="${REST#*/}"
		DB_NAME="${PATH_PART%%/*}"
	else
		DB_NAME=""
	fi
else
	DB_NAME="${NO_QUERY##*/}"
fi

# 1. Empty or unset database URL is always refused (ALLOW_SHARED_DB cannot bypass this).
if [ -z "$DB_NAME" ]; then
	cat >&2 << 'EOF'
ERROR: make test-security refused: MINICLOUD_TEST_DATABASE_URL is unset.
Security tests require a database URL so PostgreSQL test cases are executed rather than skipped.
Set MINICLOUD_TEST_DATABASE_URL in .env or the environment to an isolated database name ending in '_test'.
Example: MINICLOUD_TEST_DATABASE_URL=postgres://blazeee:minicloud@127.0.0.1:5433/minicloud_gemini_test?sslmode=disable
EOF
	exit 2
fi

# 2. Database name must end in _test; override does not bypass it.
case "$DB_NAME" in
	*_test) ;;
	*)
		cat >&2 << 'EOF'
ERROR: make test-security refused: database name does not end in "_test".
tests/internal/sectest/pg.go guardDatabaseName refuses names not ending in "_test" with a fatal safety violation.
Set MINICLOUD_TEST_DATABASE_URL in .env or the environment to an isolated database name ending in '_test'.
Example: MINICLOUD_TEST_DATABASE_URL=postgres://blazeee:minicloud@127.0.0.1:5433/minicloud_gemini_test?sslmode=disable
EOF
		exit 2
		;;
esac

# 3. Shared database minicloud_test is refused unless ALLOW_SHARED_DB=1 is set.
if [ "$DB_NAME" = "minicloud_test" ]; then
	if [ "${ALLOW_SHARED_DB:-0}" != "1" ]; then
		cat >&2 << 'EOF'
ERROR: make test-security refused: database is the shared "minicloud_test".
tests/internal/sectest/pg.go guardDatabaseName refuses the shared database with a fatal safety violation.
Set MINICLOUD_TEST_DATABASE_URL in .env or the environment to an isolated database name ending in '_test'.
Example: MINICLOUD_TEST_DATABASE_URL=postgres://blazeee:minicloud@127.0.0.1:5433/minicloud_gemini_test?sslmode=disable
To override this guard and run anyway, pass ALLOW_SHARED_DB=1 (e.g. ALLOW_SHARED_DB=1 make test-security).
EOF
		exit 2
	fi
fi

exit 0
