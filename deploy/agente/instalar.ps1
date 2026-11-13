<#
.SYNOPSIS
  Instala o agente do Invenire numa máquina Windows: cadastra no servidor e
  instala o serviço. Pode rodar de novo: o que já estiver feito é pulado, e o
  serviço é atualizado para o executável que estiver ao lado do script.

.DESCRIPTION
  De onde vem cada coisa, nesta ordem:
  - endereço e pin: -Server/-CaPin; senão, o instalar.conf ao lado do script
    (o pacote baixado do painel traz um); senão, os que o servidor preencheu
    no script que ele mesmo serve (comando de uma linha);
  - token: -Token ou INVENIRE_ENROLL_TOKEN; senão, o instalar.conf;
  - agente: -Agent; senão, invenire-agent.exe (ou invenire-agent-windows-amd64.exe)
    ao lado do script; senão, baixado do servidor (só com certificado público:
    com pin, o download não teria como conferir o servidor).
  O executável é conferido contra o SHA256SUMS antes de rodar.

.EXAMPLE
  # Pacote do painel: extrair o .zip e dar dois cliques no Instalar.cmd.

.EXAMPLE
  # PowerShell como administrador, na pasta com invenire-agent.exe:
  $env:INVENIRE_ENROLL_TOKEN = "inv_..."
  .\instalar.ps1 -Server https://invenire.exemplo.org:8443 -CaPin sha256:...

.NOTES
  O token pode ir em -Token, mas pelo ambiente ele não fica no histórico.
  Sem -CaPin, o servidor é conferido pelas CAs do sistema (certificado público).
#>
[CmdletBinding()]
param(
  [string] $Server = "",
  [string] $CaPin = "",
  [string] $Token = $env:INVENIRE_ENROLL_TOKEN,
  [string] $Agent = "",
  [string] $Interval = "4h",
  [switch] $Force
)
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue" # a barra de progresso deixa o download lento

# O servidor preenche estas duas linhas no script que ele mesmo serve.
$ServedServer = ""
$ServedPin = ""

# Rodando de um arquivo, o erro encerra com código 1 (o Instalar.cmd confere);
# rodando pelo comando de uma linha, exit fecharia a janela do PowerShell
# antes de a pessoa ler a mensagem.
function Fail([string] $msg, [string] $todo = "") {
  Write-Host ""
  Write-Host "Não deu para instalar: $msg" -ForegroundColor Red
  if ($todo) { Write-Host "O que fazer: $todo" -ForegroundColor Yellow }
  if ($PSCommandPath) { exit 1 }
  throw "instalação interrompida"
}

$here = $PSScriptRoot

# instalar.conf: linhas chave=valor; # começa comentário. Nada ali é executado.
$conf = if ($here) { Join-Path $here "instalar.conf" } else { "" }
$cfg = @{}
if ($conf -and (Test-Path -LiteralPath $conf)) {
  foreach ($line in Get-Content -LiteralPath $conf -Encoding UTF8) {
    if ($line -match '^\s*([a-z_]+)\s*=\s*(.*?)\s*$') { $cfg[$Matches[1]] = $Matches[2] }
  }
  Write-Host "configuração lida de $conf"
}
$usedConf = $false
if (-not $Server) {
  if ($cfg["servidor"]) {
    $Server, $CaPin, $usedConf = $cfg["servidor"], $cfg["ca_pin"], $true
  } elseif ($ServedServer) {
    $Server, $CaPin = $ServedServer, $ServedPin
  }
}
if (-not $Token -and $cfg["token"]) { $Token, $usedConf = $cfg["token"], $true }

$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
  [Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $admin) {
  Fail "este instalador precisa de permissão de administrador." `
    "use o Instalar.cmd do pacote (ele pede a permissão), ou abra o PowerShell com o botão direito > 'Executar como administrador'."
}

# Acha o agente; sem ele na pasta, baixa do servidor.
if (-not $Agent -and $here) {
  foreach ($n in "invenire-agent.exe", "invenire-agent-windows-amd64.exe") {
    $p = Join-Path $here $n
    if (Test-Path -LiteralPath $p) { $Agent = $p; break }
  }
}
if (-not $Agent) {
  if (-not $Server) {
    Fail "o programa do agente (invenire-agent.exe) não está na pasta e não sei o endereço do servidor para baixá-lo." `
      "baixe de novo o pacote no painel do Invenire (Adicionar máquinas) e extraia o .zip inteiro."
  }
  if ($CaPin) {
    Fail "o programa do agente (invenire-agent.exe) não está na pasta." `
      "use o pacote para Windows baixado do painel do Invenire, que já traz o programa, e extraia o .zip inteiro."
  }
  $dl = Join-Path ([IO.Path]::GetTempPath()) ("invenire-" + [guid]::NewGuid().ToString("N").Substring(0, 8))
  New-Item -ItemType Directory -Path $dl | Out-Null
  $Agent = Join-Path $dl "invenire-agent-windows-amd64.exe"
  $base = $Server.TrimEnd("/")
  Write-Host "baixando o agente de $base ..."
  try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -UseBasicParsing -Uri "$base/download/agent/windows-amd64" -OutFile $Agent
    Invoke-WebRequest -UseBasicParsing -Uri "$base/download/SHA256SUMS" -OutFile (Join-Path $dl "SHA256SUMS")
  } catch {
    Fail "não consegui baixar o agente de $base ($($_.Exception.Message))." `
      "confira se esta máquina abre $base no navegador. Se não abrir, a rede ou o firewall bloqueia o servidor; se abrir, baixe o pacote para Windows no painel e use-o."
  }
}
if (-not (Test-Path -LiteralPath $Agent)) { Fail "não achei o programa do agente em $Agent." "confira o caminho passado em -Agent." }

# Confere o executável contra o SHA256SUMS da versão, se ele estiver ao lado.
$sums = Join-Path (Split-Path $Agent) "SHA256SUMS"
if (Test-Path -LiteralPath $sums) {
  $name = Split-Path $Agent -Leaf
  $line = Get-Content -LiteralPath $sums | Where-Object { $_ -match "\s\*?$([regex]::Escape($name))$" } | Select-Object -First 1
  if (-not $line) { Fail "$name não está na lista de conferência (SHA256SUMS)." "baixe o pacote de novo no painel." }
  $want = ($line -split "\s+")[0].ToLower()
  $got = (Get-FileHash -LiteralPath $Agent -Algorithm SHA256).Hash.ToLower()
  if ($want -ne $got) {
    Fail "o programa $name está diferente do que o servidor publicou (arquivo corrompido ou alterado)." `
      "baixe o pacote de novo no painel. Se repetir, um antivírus pode estar mexendo no arquivo."
  }
  Write-Host "executável conferido: $got"
}
# Arquivo vindo da internet ganha a marca "baixado da web"; tirar evita o
# aviso do Windows ao rodar. A conferência acima é que garante o conteúdo.
Unblock-File -LiteralPath $Agent -ErrorAction SilentlyContinue

$state = Join-Path $env:ProgramData "Invenire"
$enrolled = Test-Path (Join-Path $state "agent.json")
if ($enrolled -and -not $Force) {
  Write-Host "esta máquina já está cadastrada ($state); o cadastro foi pulado (-Force cadastra de novo e revoga o anterior)"
} else {
  if (-not $Server) {
    Fail "não sei o endereço do servidor." `
      "use o pacote baixado do painel (ele traz o instalar.conf), ou passe -Server https://... (e -CaPin, se o painel mostrar um pin)."
  }
  if (-not $Token) {
    Fail "falta o token de cadastro." `
      "use o pacote baixado do painel, ou crie um token no painel (Adicionar máquinas) e defina INVENIRE_ENROLL_TOKEN antes de rodar."
  }
  $enrollArgs = @("enroll", "--server", $Server)
  if ($CaPin) { $enrollArgs += @("--ca-pin", $CaPin) }
  if ($Force) { $enrollArgs += "--force" }
  $env:INVENIRE_ENROLL_TOKEN = $Token
  & $Agent @enrollArgs
  $rc = $LASTEXITCODE
  Remove-Item Env:INVENIRE_ENROLL_TOKEN -ErrorAction SilentlyContinue
  if ($rc -ne 0) {
    Fail "o servidor não aceitou o cadastro (a mensagem do agente está logo acima)." `
      "se falar em token, ele venceu, foi revogado ou já cadastrou todas as máquinas que podia: crie outro no painel e baixe o pacote de novo. Se falar em conexão, confira se esta máquina abre $Server no navegador. Confira também se a data e a hora do computador estão certas."
  }
}

& $Agent service install --interval $Interval
if ($LASTEXITCODE -ne 0) { Fail "o serviço do agente não pôde ser instalado (mensagem acima)." "rode de novo como administrador; se repetir, um antivírus pode estar bloqueando o invenire-agent.exe." }

Start-Sleep -Seconds 3
$svc = Get-Service invenire-agent -ErrorAction SilentlyContinue
if (-not $svc -or $svc.Status -ne "Running") {
  Fail "o serviço invenire-agent foi instalado mas não está rodando." `
    "veja o Visualizador de Eventos > Logs do Windows > Aplicativo, origem invenire-agent, e mostre a quem cuida do Invenire."
}
Write-Host "pronto: serviço invenire-agent rodando; a primeira coleta já foi enviada ou está na fila" -ForegroundColor Green

# O instalar.conf tem o token. Token de uma máquina só: apaga. Token de várias
# máquinas (o mesmo pacote num pendrive): avisa, porque ainda serve.
if ($usedConf) {
  if ($cfg["apagar_depois"] -eq "sim") {
    try {
      Remove-Item -LiteralPath $conf -Force
      Write-Host "o arquivo instalar.conf, que tinha o token de cadastro, foi apagado"
    } catch {
      Write-Host "Atenção: não consegui apagar $conf, que tem o token de cadastro. Apague o arquivo à mão." -ForegroundColor Yellow
    }
  } else {
    Write-Host "Atenção: o instalar.conf desta pasta tem um token que ainda cadastra outras máquinas." -ForegroundColor Yellow
    Write-Host "Guarde a pasta em lugar seguro e apague-a quando terminar; se ela se perder, revogue o token no painel." -ForegroundColor Yellow
  }
}
