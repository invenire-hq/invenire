#!/bin/sh
# Backup do servidor do piloto: o banco (pg_dump) e os dados do servidor
# (CA da instalação e chave-mestra). Um sem o outro não restaura: sem a
# chave-mestra o servidor não emite crachás; sem a CA os agentes deixam de
# reconhecer o servidor.
#
#   ./backup.sh [diretório]      (padrão: ./backup)
#
# Rode na pasta deploy/piloto, com o compose de pé. Para todo dia, no cron:
#   0 2 * * * cd /opt/invenire/deploy/piloto && ./backup.sh /var/backups/invenire
set -eu

dest=${1:-./backup}
stamp=$(date +%Y%m%d-%H%M%S)
dir="$dest/$stamp"
mkdir -p "$dir"
chmod 700 "$dest" "$dir"
# Backup pela metade não serve: se algo falhar, ele é apagado.
trap 'status=$?; [ $status -eq 0 ] || { rm -rf "$dir"; echo "backup falhou; nada gravado" >&2; }' EXIT

docker compose exec -T postgres pg_dump -U invenire -d invenire -Fc > "$dir/invenire.dump"
docker compose cp server:/var/lib/invenire/data "$dir/data" >/dev/null
( cd "$dir" && find . -type f ! -name SHA256SUMS -exec sha256sum {} + > SHA256SUMS )

echo "backup em $dir"
echo "guarde junto com ele a senha do banco (.env); a chave-mestra está em $dir/data/master.key"

# Mantém os 14 mais novos.
ls -1d "$dest"/[0-9]*-[0-9]* 2>/dev/null | sort | head -n -14 | while read -r old; do rm -rf "$old"; done
