#!/bin/sh
# Monta uma versão para distribuir: agente e servidor para Windows e Linux,
# os scripts de instalação do agente e o SHA256SUMS de tudo.
#
#   scripts/build.sh 0.1.0        -> dist/0.1.0/
#
# A pasta inteira vai para o compartilhamento de onde as máquinas instalam;
# os scripts conferem o executável contra o SHA256SUMS antes de cadastrar.
set -eu

version=${1:?uso: scripts/build.sh <versão>}
root=$(cd "$(dirname "$0")/.." && pwd)
out="$root/dist/$version"
rm -rf "$out"
mkdir -p "$out"

build() { # os arch nome pacote
  ext=""
  [ "$1" = windows ] && ext=".exe"
  echo "  $3-$1-$2$ext"
  (cd "$root" && CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath \
    -ldflags "-s -w -X main.version=$version" -o "$out/$3-$1-$2$ext" "./cmd/$3")
}

echo "invenire $version"
build windows amd64 invenire-agent
build linux amd64 invenire-agent
build linux arm64 invenire-agent
build linux amd64 invenire-server
build windows amd64 invenire-server

# Os scripts procuram o agente com estes nomes ao lado deles.
cp "$out/invenire-agent-windows-amd64.exe" "$out/invenire-agent.exe"
cp "$root/deploy/agente/instalar.ps1" "$root/deploy/agente/instalar.sh" "$out/"
chmod 755 "$out"/invenire-agent-linux-* "$out"/invenire-server-linux-* "$out/instalar.sh"

(cd "$out" && sha256sum -- * | grep -v ' SHA256SUMS$' > SHA256SUMS)
echo "pronto: $out"
cat "$out/SHA256SUMS"
