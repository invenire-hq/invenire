#!/bin/sh
# Instala o agente do Invenire numa máquina Linux com systemd: cadastra no
# servidor e instala o serviço. Pode rodar de novo: o que já estiver feito é
# pulado, e o serviço é atualizado para o executável ao lado do script.
#
#   Pacote do painel:  tar xzf invenire-agente-linux.tar.gz && sudo ./invenire-agente/instalar.sh
#   Pasta da versão:   sudo INVENIRE_ENROLL_TOKEN=inv_... ./instalar.sh --server https://invenire.exemplo.org:8443 --ca-pin sha256:...
#
# De onde vem cada coisa, nesta ordem:
# - endereço e pin: --server/--ca-pin; senão, o instalar.conf ao lado do
#   script (o pacote do painel traz um); senão, os que o servidor preencheu no
#   script que ele mesmo serve (comando de uma linha);
# - token: INVENIRE_ENROLL_TOKEN; senão, o instalar.conf;
# - agente: --agent; senão, invenire-agent-linux-<arq> (ou invenire-agent) ao
#   lado do script; senão, baixado do servidor (só com certificado público:
#   com pin, o download não teria como conferir o servidor).
# O executável é conferido contra o SHA256SUMS antes de rodar.
#
# Opções: --server URL, --ca-pin sha256:..., --agent CAMINHO, --interval 4h, --force.
set -eu

# O servidor preenche estas duas linhas no script que ele mesmo serve.
served_server=""
served_pin=""

fail() { # mensagem [o que fazer]
  echo >&2
  echo "Não deu para instalar: $1" >&2
  [ $# -lt 2 ] || echo "O que fazer: $2" >&2
  exit 1
}

server="" pin="" interval="4h" force="" agent="" token=${INVENIRE_ENROLL_TOKEN:-}
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) arch=$(uname -m) ;;
esac

while [ $# -gt 0 ]; do
  case "$1" in
    --server) server=${2:-}; shift 2 ;;
    --ca-pin) pin=${2:-}; shift 2 ;;
    --agent) agent=${2:-}; shift 2 ;;
    --interval) interval=${2:-}; shift 2 ;;
    --force) force=1; shift ;;
    *) fail "opção desconhecida: $1" "as opções são --server, --ca-pin, --agent, --interval e --force." ;;
  esac
done

# Vindo de um pipe (curl ... | sh), a "pasta do script" é a pasta onde a
# pessoa está: não se procura nada ali.
dir=""
[ -n "$served_server" ] || dir=$(cd "$(dirname "$0")" && pwd)

# instalar.conf: linhas chave=valor; # começa comentário. É lido, nunca
# executado (nada de ".").
conf="" conf_server="" conf_pin="" conf_token="" conf_delete="" used_conf=""
if [ -n "$dir" ] && [ -f "$dir/instalar.conf" ]; then
  conf="$dir/instalar.conf"
  while IFS= read -r line || [ -n "$line" ]; do
    line=$(printf '%s' "$line" | tr -d '\r')
    case "$line" in '#'* | '') continue ;; esac
    key=$(printf '%s' "${line%%=*}" | tr -d ' \t')
    val=$(printf '%s' "${line#*=}" | sed 's/^[ \t]*//; s/[ \t]*$//')
    case "$key" in
      servidor) conf_server=$val ;;
      ca_pin) conf_pin=$val ;;
      token) conf_token=$val ;;
      apagar_depois) conf_delete=$val ;;
    esac
  done < "$conf"
  echo "configuração lida de $conf"
fi
if [ -z "$server" ]; then
  if [ -n "$conf_server" ]; then
    server=$conf_server pin=$conf_pin used_conf=1
  elif [ -n "$served_server" ]; then
    server=$served_server pin=$served_pin
  fi
fi
if [ -z "$token" ] && [ -n "$conf_token" ]; then
  token=$conf_token used_conf=1
fi

[ "$(id -u)" -eq 0 ] || fail "este instalador precisa rodar como root." "rode de novo com sudo na frente (sudo ./instalar.sh)."
command -v systemctl >/dev/null ||
  fail "este sistema não usa systemd, e o instalador só sabe criar serviço nele." "rode 'invenire-agent run' por outro supervisor de processos."

# Acha o agente; sem ele na pasta, baixa do servidor.
if [ -z "$agent" ] && [ -n "$dir" ]; then
  for n in "invenire-agent-linux-$arch" invenire-agent; do
    if [ -f "$dir/$n" ]; then agent="$dir/$n"; break; fi
  done
fi
if [ -z "$agent" ]; then
  [ -n "$server" ] || fail "o programa do agente não está na pasta e não sei o endereço do servidor para baixá-lo." \
    "baixe de novo o pacote para Linux no painel do Invenire (Adicionar máquinas) e extraia o arquivo inteiro."
  [ -z "$pin" ] || fail "o programa do agente (invenire-agent-linux-$arch) não está na pasta." \
    "use o pacote para Linux baixado do painel do Invenire, que já traz o programa."
  case "$arch" in amd64 | arm64) ;; *) fail "não há agente para este processador ($arch)." "o Invenire tem agente para Linux amd64 e arm64." ;; esac
  dl=$(mktemp -d)
  trap 'rm -rf "$dl"' EXIT # o service install copia o executável para /usr/local/bin
  agent="$dl/invenire-agent-linux-$arch"
  base=${server%/}
  echo "baixando o agente de $base ..."
  get() { # url arquivo
    if command -v curl >/dev/null; then curl -fsSL --proto '=https' -o "$2" "$1"
    elif command -v wget >/dev/null; then wget -q -O "$2" "$1"
    else fail "não há curl nem wget para baixar o agente." "instale o curl (apt install curl, dnf install curl) e rode de novo."
    fi
  }
  get "$base/download/agent/linux-$arch" "$agent" && get "$base/download/SHA256SUMS" "$dl/SHA256SUMS" ||
    fail "não consegui baixar o agente de $base." \
      "confira se esta máquina alcança o servidor (curl -I $base/healthz). Se não alcançar, a rede ou o firewall bloqueia; se alcançar, baixe o pacote para Linux no painel e use-o."
fi
[ -f "$agent" ] || fail "não achei o programa do agente em $agent." "confira o caminho passado em --agent."

# Confere o executável contra o SHA256SUMS da versão, se ele estiver ao lado.
sums="$(dirname "$agent")/SHA256SUMS"
if [ -f "$sums" ]; then
  name=$(basename "$agent")
  want=$(awk -v n="$name" '{f=$2; sub(/^\*/, "", f)} f == n {print $1}' "$sums")
  [ -n "$want" ] || fail "$name não está na lista de conferência (SHA256SUMS)." "baixe o pacote de novo no painel."
  got=$(sha256sum "$agent" | cut -d' ' -f1)
  [ "$want" = "$got" ] || fail "o programa $name está diferente do que o servidor publicou (arquivo corrompido ou alterado)." \
    "baixe o pacote de novo no painel."
  echo "executável conferido: $got"
fi
# Num compartilhamento só leitura o chmod falha; só tenta se precisar.
[ -x "$agent" ] || chmod 755 "$agent" 2>/dev/null ||
  fail "$agent não pode ser executado e não dá para mudar isso (compartilhamento só leitura?)." "copie a pasta para a máquina e rode de lá."

if [ -f /var/lib/invenire/agent.json ] && [ -z "$force" ]; then
  echo "esta máquina já está cadastrada (/var/lib/invenire); o cadastro foi pulado (--force cadastra de novo e revoga o anterior)"
else
  [ -n "$server" ] || fail "não sei o endereço do servidor." \
    "use o pacote baixado do painel (ele traz o instalar.conf), ou passe --server https://... (e --ca-pin, se o painel mostrar um pin)."
  [ -n "$token" ] || fail "falta o token de cadastro." \
    "use o pacote baixado do painel, ou crie um token no painel (Adicionar máquinas) e defina INVENIRE_ENROLL_TOKEN."
  set -- enroll --server "$server"
  [ -z "$pin" ] || set -- "$@" --ca-pin "$pin"
  [ -z "$force" ] || set -- "$@" --force
  INVENIRE_ENROLL_TOKEN=$token "$agent" "$@" ||
    fail "o servidor não aceitou o cadastro (a mensagem do agente está logo acima)." \
      "se falar em token, ele venceu, foi revogado ou já cadastrou todas as máquinas que podia: crie outro no painel e baixe o pacote de novo. Se falar em conexão, confira se esta máquina alcança $server. Confira também a data e a hora da máquina (date)."
fi

"$agent" service install --interval "$interval" ||
  fail "o serviço do agente não pôde ser instalado (mensagem acima)." "rode de novo como root; se repetir, mostre a mensagem a quem cuida do Invenire."

sleep 3
systemctl is-active --quiet invenire-agent ||
  fail "o serviço invenire-agent foi instalado mas não está ativo." "veja o motivo com: journalctl -u invenire-agent"
echo "pronto: serviço invenire-agent ativo; a primeira coleta já foi enviada ou está na fila"

# O instalar.conf tem o token. Token de uma máquina só: apaga. Token de várias
# máquinas (o mesmo pacote copiado para outras): avisa, porque ainda serve.
if [ -n "$used_conf" ]; then
  if [ "$conf_delete" = sim ]; then
    if rm -f "$conf" 2>/dev/null && [ ! -e "$conf" ]; then
      echo "o arquivo instalar.conf, que tinha o token de cadastro, foi apagado"
    else
      echo "Atenção: não consegui apagar $conf, que tem o token de cadastro. Apague o arquivo à mão." >&2
    fi
  else
    echo "Atenção: o instalar.conf desta pasta tem um token que ainda cadastra outras máquinas." >&2
    echo "Guarde a pasta em lugar seguro e apague-a quando terminar; se ela se perder, revogue o token no painel." >&2
  fi
fi
