#!/bin/bash
set -e

# Initializes the secondary test database (minicloud_test) on fresh volume creation.
# NOTE: Official postgres docker entrypoint only runs scripts in /docker-entrypoint-initdb.d
# when the database volume is completely fresh/empty.

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    SELECT 'CREATE DATABASE minicloud_test'
    WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'minicloud_test')\gexec
    GRANT ALL PRIVILEGES ON DATABASE minicloud_test TO "$POSTGRES_USER";
EOSQL
