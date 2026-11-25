Português | [English](en/agents.md)

# Agentes

O agente (`invenire-agent`) roda como serviço em cada máquina inventariada: coleta hardware,
sistema e software instalado na subida e depois a cada 4 horas, guarda numa fila local e envia
ao servidor. Há agente para **Windows** (amd64) e **Linux** com systemd (amd64 e arm64).

O servidor distribui o agente: a imagem Docker traz os executáveis com o `SHA256SUMS`, e o
painel monta com eles o pacote do instalador. Ninguém precisa de Go nem de compartilhamento de
rede.

## Instalar

Tudo começa em **Adicionar máquinas**, no painel: crie um token de cadastro (unidade, quantas
máquinas, validade). A tela mostra o token uma vez e três formas de instalar.

### 1. Pacote para baixar (o mais fácil)

Um `.zip` (Windows) ou `.tar.gz` (Linux) montado na hora, com o agente, o instalador, o
`SHA256SUMS` e um `instalar.conf` com o endereço do servidor, o pin (se houver) e o token. Não há
nada a digitar, funciona com ou sem pin e sem internet na máquina (por pendrive, por exemplo).

**Windows:** botão direito no `.zip` > **Extrair tudo**; na pasta `invenire-agente`, dois
cliques em `Instalar.cmd` e **Sim** quando o Windows pedir permissão de administrador. O
`Instalar.cmd` chama o `instalar.ps1` da mesma pasta com `-ExecutionPolicy Bypass`.

**Linux:**

```sh
tar xzf invenire-agente-linux.tar.gz
sudo ./invenire-agente/instalar.sh
```

O pacote do Linux traz o agente amd64 e o arm64; o instalador escolhe pelo processador.

Depois de instalar, se o token era de uma máquina só, o instalador apaga o `instalar.conf` (que
tem o token). Se o token ainda cadastra outras, o mesmo pacote serve a todas: o instalador
avisa, e você apaga o pacote quando o lote terminar. **O pacote contém o token**: trate-o como
uma senha, com prazo curto e poucos usos, e revogue o token no painel se ele se perder.

### 2. Comando de uma linha

Só quando o servidor tem certificado público ([VPS](instalar/vps.md) ou
[proxy reverso](instalar/proxy-reverso.md)): com pin, o download pela linha de comando não teria
como conferir o servidor, e os scripts se recusam a baixar o agente. A tela do token mostra o
comando pronto:

```sh
curl -fsSL https://invenire.exemplo.org/download/instalar.sh | sudo INVENIRE_ENROLL_TOKEN=inv_… sh
```

No Windows, no PowerShell **como administrador**:

```powershell
$env:INVENIRE_ENROLL_TOKEN = "inv_…"; [Net.ServicePointManager]::SecurityProtocol = 'Tls12'; & ([scriptblock]::Create((irm https://invenire.exemplo.org/download/instalar.ps1)))
```

O token vai numa variável de ambiente, nunca na URL (que ficaria no log do proxy). O script
servido já traz o endereço do servidor, baixa o agente de `/download/agent/…`, confere contra o
`/download/SHA256SUMS` e instala.

### 3. Manual

Para quem quer ver cada passo, com os executáveis de uma pasta de versão (gerada por
`scripts/build.sh`, veja [desenvolvimento.md](desenvolvimento.md#versão-para-distribuir)) ou
baixados de `/download/agent/windows-amd64`, `/download/agent/linux-amd64` ou
`/download/agent/linux-arm64`. Como administrador (Windows) ou root (Linux):

```sh
invenire-agent enroll --server https://192.168.0.10:8443 --ca-pin sha256:… --token inv_…
invenire-agent service install
```

Sem `--ca-pin` quando o servidor tem certificado público. O token também pode ir em
`INVENIRE_ENROLL_TOKEN`, para não ficar no histórico do terminal nem na lista de processos.

Os scripts da pasta da versão fazem o mesmo, conferindo o executável antes:

```powershell
$env:INVENIRE_ENROLL_TOKEN = "inv_…"
.\instalar.ps1 -Server https://192.168.0.10:8443 -CaPin sha256:…
```

```sh
sudo INVENIRE_ENROLL_TOKEN=inv_… ./instalar.sh --server https://192.168.0.10:8443 --ca-pin sha256:…
```

Opções dos scripts: `-Server`/`--server`, `-CaPin`/`--ca-pin`, `-Agent`/`--agent` (caminho do
executável), `-Interval`/`--interval` (padrão `4h`) e `-Force`/`--force` (cadastra de novo e
revoga o crachá anterior da máquina). De onde vem cada coisa, nesta ordem: o parâmetro; o
`instalar.conf` ao lado do script; o que o servidor preencheu no script que ele mesmo serve.

### Windows: SmartScreen e antivírus

Os executáveis ainda não têm assinatura de código. Ao abrir o `Instalar.cmd` baixado da
internet, o Windows pode mostrar **"O Windows protegeu o computador"**: clique em **Mais
informações** e em **Executar assim mesmo**. Algum antivírus pode bloquear o
`invenire-agent.exe`; nesse caso, libere o arquivo no antivírus. Os scripts conferem o
executável contra o `SHA256SUMS` antes de rodar: um executável alterado é recusado.

## Conferir

O instalador termina com `pronto: serviço invenire-agent ativo; a primeira coleta já foi
enviada ou está na fila`. Em alguns segundos a máquina aparece em **Visão geral** com `em-dia`.
Se aparecer outro estado, veja [problemas.md](problemas.md).

| | Windows | Linux |
|---|---|---|
| Executável | `%ProgramFiles%\Invenire\invenire-agent.exe` | `/usr/local/bin/invenire-agent` |
| Serviço | `invenire-agent`, automático (início atrasado), conta SYSTEM | `/etc/systemd/system/invenire-agent.service`, root, habilitada |
| Ver se está rodando | `Get-Service invenire-agent` | `systemctl status invenire-agent` |
| Log | Visualizador de Eventos > Aplicativo, origem `invenire-agent` | `journalctl -u invenire-agent` |
| Estado (chave, crachá, fila) | `C:\ProgramData\Invenire` | `/var/lib/invenire` |
| Se o processo cair | reinicia em 1, 5 e 15 min | reinicia em 60 s |
| Crachá revogado (saída 3) | para e não reinicia | para e não reinicia (`RestartPreventExitStatus`) |

A unit do systemd roda como root, mas com o sistema protegido: `ProtectSystem=full`,
`ProtectHome=read-only`, `NoNewPrivileges`, sem acesso a dispositivos, módulos ou tunables do
kernel, e só sockets IP e Unix. Ela é gerada a partir de
`cmd/invenire-agent/invenire-agent.service.tmpl`.

## Atualizar

Rode o instalador de novo, com a versão nova: ele vê que a máquina já está cadastrada, pula o
cadastro e troca o executável do serviço. Não precisa de token.

- Linux, com certificado público: `curl -fsSL https://invenire.exemplo.org/download/instalar.sh | sudo sh`
  (o servidor entrega o agente da versão dele).
- Com o pacote: baixe um pacote novo do painel depois de atualizar o servidor (o painel só monta
  pacote para um token válido; um token de um uso e prazo curto serve, e não é gasto numa
  máquina já cadastrada) e rode o `Instalar.cmd` ou o `instalar.sh` dele.
- Com a pasta da versão: rode o `instalar.ps1` ou o `instalar.sh` dela.

Por baixo, é `invenire-agent service install` com o executável novo: para o serviço, troca o
executável e a configuração e o inicia de novo.

## Trocar o servidor

Quando o servidor muda de endereço (de rede local para proxy, ou para outra máquina com a mesma
organização), o agente troca de endereço sem recadastrar. Como administrador ou root:

```sh
invenire-agent set-server --server https://invenire.exemplo.org                       # certificado público
invenire-agent set-server --server https://192.168.0.20:8443 --ca-pin sha256:…       # com pin
```

No Windows, o executável é `& "$env:ProgramFiles\Invenire\invenire-agent.exe"`. Antes de
gravar, o `set-server` pergunta ao servidor novo (`GET /v1/agent`) se ele reconhece o crachá; se
não reconhecer, ou se o pin não conferir, nada muda. Depois, reinicie o serviço
(`Restart-Service invenire-agent` ou `systemctl restart invenire-agent`).

O crachá vem da CA da organização, que vai junto com ela (backup e restauração, ou exportação
com a chave da CA): por isso o mesmo crachá vale no servidor novo.

## Desinstalar

Windows (PowerShell como administrador):

```powershell
& "$env:ProgramFiles\Invenire\invenire-agent.exe" service uninstall
```

Linux:

```sh
sudo /usr/local/bin/invenire-agent service uninstall
```

O executável e o estado (`C:\ProgramData\Invenire` ou `/var/lib/invenire`) ficam; apague a pasta
de estado para sumir com o crachá. No painel, em **Máquinas**, revogue o agente para o servidor
parar de aceitá-lo.

## Como o agente funciona

**Coleta sem servidor**, para conferir o que o agente vê ou para redes isoladas:

```sh
invenire-agent collect --out snapshot.json
```

No Linux, rode como root: `product_uuid` e `product_serial` só são legíveis por ele.

**Cadastro e envio**, à mão (o serviço faz o `run`):

```sh
invenire-agent enroll --server https://invenire.local:8443 --token inv_… --ca-pin sha256:…
invenire-agent run               # coleta a cada 4h (--interval) e envia
invenire-agent run --once        # uma coleta e uma tentativa de envio
```

No primeiro contato, com pin, o agente só aceita o servidor se a cadeia TLS trouxer a CA com
aquele SHA-256; depois, só confia nela. Sem pin, confere o servidor pelas CAs do sistema, como um
navegador. O agente exige TLS 1.3.

**Estado.** Em `%ProgramData%\Invenire` ou `/var/lib/invenire` (`--state-dir`): chave,
certificado (o crachá), CA, `agent.json`, o hash do último snapshot (para o `prev_sha256`) e a
fila em `queue/`. No Windows o diretório tem dono Administradores e acesso só de SYSTEM e
Administradores; no Linux, 0700.

**Fila.** Guarda até 100 snapshots (`--max-queue`) com os bytes exatos que serão enviados; o
nome de cada arquivo traz o SHA-256, conferido antes do envio. O que o servidor recusa de vez vai
para `queue/rejected/` com o motivo. Se o servidor recusar o crachá em si (agente revogado ou
desconhecido, crachá vencido), `run` para com o código de saída 3 e a fila fica intacta. Outro
`401`, e um `413` que não venha do próprio servidor, costumam ser de um proxy no caminho: o
snapshot fica na fila e o agente tenta de novo, com espera crescente.

**Autenticação.** O agente apresenta o crachá no TLS (mTLS) e, além disso, assina toda
requisição com a chave dele (`Invenire-Agent-Cert`, `Invenire-Timestamp`,
`Invenire-Signature`). A assinatura é o que passa por um proxy que termina o TLS; veja
[seguranca.md](seguranca.md#o-agente-e-o-servidor).

**Renovação.** O crachá vale dois anos e o agente o renova sozinho quando faltam oito meses
(`POST /v1/agent/renew`), com a mesma chave. Se a renovação falhar, o crachá atual continua em
uso e a coleta seguinte tenta de novo. Só uma máquina que ficou desligada até o crachá vencer
precisa de um `enroll --force`.

**Opções do serviço:**

```sh
invenire-agent service install                 # --interval, --state-dir, --max-queue, --bin
invenire-agent service uninstall               # executável e estado ficam
```

`install` copia o executável para um lugar que só administradores alteram (`--bin`), registra o
serviço `invenire-agent` rodando `run` com as opções dadas e o inicia.
