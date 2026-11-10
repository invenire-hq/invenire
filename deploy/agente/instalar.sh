#!/bin/sh
# Instala o agente do Invenire numa máquina Linux com systemd: cadastra no
# servidor e instala o serviço. Pode rodar de novo: o que já estiver feito é
# pulado, e o serviço é atualizado para o executável ao lado do script.
#
#   sudo INVENIRE_ENROLL_TOKEN=inv_... ./instalar.sh --server https://invenire.prefeitura.local:8443 --ca-pin sha256:...
#
# Opções: --server URL (obrigatório), --ca-pin sha256:... (sem ele, o servidor
# é conferido pelas CAs do sistema), --agent CAMINHO, --interval 4h, --force.
set -eu

fail() { echo "erro: $*" >&2; exit 1; }

dir=$(cd "$(dirname "$0")" && pwd)
server="" pin="" interval="4h" force=""
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) arch=$(uname -m) ;;
esac
agent="$dir/invenire-agent-linux-$arch"
[ -f "$agent" ] || agent="$dir/invenire-agent"

while [ $# -gt 0 ]; do
  case "$1" in
    --server) server=$2; shift 2 ;;
    --ca-pin) pin=$2; shift 2 ;;
    --agent) agent=$2; shift 2 ;;
    --interval) interval=$2; shift 2 ;;
    --force) force=1; shift ;;
    *) fail "opção desconhecida: $1" ;;
  esac
done

[ -n "$server" ] || fail "--server é obrigatório"
[ "$(id -u)" -eq 0 ] || fail "rode como root (sudo)"
[ -f "$agent" ] || fail "não achei $agent"
command -v systemctl >/dev/null || fail "sem systemd: rode 'invenire-agent run' por outro supervisor"

# Confere o executável contra o SHA256SUMS da versão, se ele estiver ao lado.
sums="$(dirname "$agent")/SHA256SUMS"
if [ -f "$sums" ]; then
  name=$(basename "$agent")
  want=$(awk -v n="$name" '{f=$2; sub(/^\*/, "", f)} f == n {print $1}' "$sums")
  [ -n "$want" ] || fail "$name não está em SHA256SUMS"
  got=$(sha256sum "$agent" | cut -d' ' -f1)
  [ "$want" = "$got" ] || fail "o SHA-256 de $name não confere com SHA256SUMS: arquivo alterado ou corrompido"
  echo "executável conferido: $got"
fi
# Num compartilhamento só leitura o chmod falha; só tenta se precisar.
[ -x "$agent" ] || chmod 755 "$agent" 2>/dev/null ||
  fail "$agent não é executável e não dá para mudar (compartilhamento só leitura?): copie a pasta para a máquina"

if [ -f /var/lib/invenire/agent.json ] && [ -z "$force" ]; then
  echo "já cadastrado (/var/lib/invenire); pulando o cadastro (--force cadastra de novo e revoga o anterior)"
else
  [ -n "${INVENIRE_ENROLL_TOKEN:-}" ] || fail "falta o token: defina INVENIRE_ENROLL_TOKEN"
  set -- enroll --server "$server"
  [ -n "$pin" ] && set -- "$@" --ca-pin "$pin"
  [ -n "$force" ] && set -- "$@" --force
  INVENIRE_ENROLL_TOKEN=$INVENIRE_ENROLL_TOKEN "$agent" "$@" ||
    fail "o cadastro falhou (veja a mensagem acima e a tabela 'Quando dá errado' do README)"
fi

"$agent" service install --interval "$interval" || fail "a instalação do serviço falhou"

sleep 3
systemctl is-active --quiet invenire-agent ||
  fail "o serviço invenire-agent não está ativo; veja: journalctl -u invenire-agent"
echo "pronto: serviço invenire-agent ativo; a primeira coleta já foi enviada ou está na fila"
