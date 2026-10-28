#!/bin/bash
# Creates additional databases and users on first Postgres container creation.
# Runs automatically via /docker-entrypoint-initdb.d/.
# NOTE: passwords are interpolated directly into SQL — do not use single quotes
# or special SQL characters. Fine for local dev only.
set -e

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    CREATE USER ${HYDRA_DB_USER:-hydra} WITH PASSWORD '${HYDRA_DB_PASSWORD:-dev}';
    CREATE DATABASE hydra OWNER ${HYDRA_DB_USER:-hydra};

    CREATE USER ${AUTH_DB_USER:-baguette_auth} WITH PASSWORD '${AUTH_DB_PASSWORD:-dev}';
    CREATE DATABASE baguettedb_auth OWNER ${AUTH_DB_USER:-baguette_auth};

    CREATE USER ${BDBS_DB_USER:-baguette_bdbs} WITH PASSWORD '${BDBS_DB_PASSWORD:-dev}';
    CREATE DATABASE baguettedb_bdbs OWNER ${BDBS_DB_USER:-baguette_bdbs};

    CREATE USER ${DATABASES_DB_USER:-baguette_databases} WITH PASSWORD '${DATABASES_DB_PASSWORD:-dev}';
    CREATE DATABASE baguettedb_databases OWNER ${DATABASES_DB_USER:-baguette_databases};
EOSQL

# Enables uuid-ossp on the hydra database (required by Hydra v26.2.0+).
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "hydra" <<-EOSQL
    CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
EOSQL
