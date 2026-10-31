#!/bin/sh
set -e

# Override: ALLOW_SHARED_DB=1 permits running against the shared database.
if [ "${ALLOW_SHARED_DB:-0}" = "1" ]; then
	exit 0
fi

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

if [ -z "$DB_NAME" ]; then
	cat >&2 << 'EOF'
ERROR: make test-security refused: MINICLOUD_TEST_DATABASE_URL is unset.
Security tests require a dedicated per-agent database to avoid cross-worktree interference (sectest rejects minicloud_test by design).
Set MINICLOUD_TEST_DATABASE_URL in .env or the environment to an isolated database name ending in '_test'.
Example: MINICLOUD_TEST_DATABASE_URL=postgres://blazeee:minicloud@127.0.0.1:5433/minicloud_gemini_test?sslmode=disable
To override this guard and run anyway, pass ALLOW_SHARED_DB=1 (e.g. ALLOW_SHARED_DB=1 make test-security).
EOF
	exit 2
fi

if [ "$DB_NAME" = "minicloud_test" ]; then
	cat >&2 << 'EOF'
ERROR: make test-security refused: database is the shared "minicloud_test".
Security tests require a dedicated per-agent database to avoid cross-worktree interference (sectest rejects minicloud_test by design).
Set MINICLOUD_TEST_DATABASE_URL in .env or the environment to an isolated database name ending in '_test'.
Example: MINICLOUD_TEST_DATABASE_URL=postgres://blazeee:minicloud@127.0.0.1:5433/minicloud_gemini_test?sslmode=disable
To override this guard and run anyway, pass ALLOW_SHARED_DB=1 (e.g. ALLOW_SHARED_DB=1 make test-security).
EOF
	exit 2
fi

exit 0
