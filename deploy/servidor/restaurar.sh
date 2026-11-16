#!/bin/sh
# Restaura um backup feito por backup.sh num compose novo (máquina nova ou
# volumes vazios). Apaga o que houver no banco e nos dados do servidor.
#
#   ./restaurar.sh ./backup/20261110-161950
#
# Rode na pasta deploy/servidor, com o .env preenchido (a senha do banco
# pode ser outra: o dump não a carrega; COMPOSE_FILE diz o cenário).
set -eu

src=${1:?uso: ./restaurar.sh <pasta do backup>}
[ -f "$src/invenire.dump" ] && [ -f "$src/data/master.key" ] || {
  echo "erro: $src não tem invenire.dump e data/master.key" >&2; exit 1; }
(cd "$src" && sha256sum -c SHA256SUMS >/dev/null) || {
  echo "erro: o backup não confere com o SHA256SUMS dele" >&2; exit 1; }

project=${COMPOSE_PROJECT_NAME:-invenire-servidor}
echo "restaurando $src no projeto $project"

docker compose stop server 2>/dev/null || true
docker compose up -d --wait postgres
docker compose exec -T postgres pg_restore -U invenire -d invenire --clean --if-exists --no-owner < "$src/invenire.dump"

# Os dados do servidor vão para o volume com o dono do processo (nonroot,
# uid 65532), que é quem lê a chave-mestra.
docker compose create server >/dev/null
abs=$(cd "$src/data" && pwd -W 2>/dev/null || pwd)
MSYS_NO_PATHCONV=1 docker run --rm -v "${project}_data:/v" -v "$abs:/src:ro" alpine:3.20 sh -c \
  'rm -rf /v/data && mkdir -p /v/data && cp -a /src/. /v/data/ && chown -R 65532:65532 /v && chmod 700 /v/data && chmod 600 /v/data/master.key /v/data/ca/ca.key'

docker compose up -d --wait
echo "restaurado; confira com: docker compose exec server invenire-server status --tenant <nome>"
