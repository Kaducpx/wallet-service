#!/bin/sh
# Cria o papel restrito usado pela aplicação em tempo de execução.
# As tabelas pertencem a POSTGRES_USER (dono, usado só pelas migrations);
# os privilégios do wallet_app são concedidos pela migration 000002.
set -eu

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<EOSQL
DO \$\$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'wallet_app') THEN
        CREATE ROLE wallet_app LOGIN PASSWORD '${POSTGRES_APP_PASSWORD}';
    END IF;
END
\$\$;
EOSQL
