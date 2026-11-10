<#
.SYNOPSIS
  Instala o agente do Invenire numa máquina Windows: cadastra no servidor e
  instala o serviço. Pode rodar de novo: o que já estiver feito é pulado, e o
  serviço é atualizado para o executável que estiver ao lado do script.

.EXAMPLE
  # PowerShell como administrador, na pasta com invenire-agent.exe:
  $env:INVENIRE_ENROLL_TOKEN = "inv_..."
  .\instalar.ps1 -Server https://invenire.prefeitura.local:8443 -CaPin sha256:...

.NOTES
  O token pode ir em -Token, mas pelo ambiente ele não fica no histórico.
  Sem -CaPin, o servidor é conferido pelas CAs do sistema (certificado público).
#>
[CmdletBinding()]
param(
  [Parameter(Mandatory)] [string] $Server,
  [string] $CaPin = "",
  [string] $Token = $env:INVENIRE_ENROLL_TOKEN,
  [string] $Agent = (Join-Path $PSScriptRoot "invenire-agent.exe"),
  [string] $Interval = "4h",
  [switch] $Force
)
$ErrorActionPreference = "Stop"

function Fail($msg) { Write-Host "erro: $msg" -ForegroundColor Red; exit 1 }

$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
  [Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $admin) { Fail "rode o PowerShell como administrador" }
if (-not (Test-Path $Agent)) { Fail "não achei $Agent" }

# Confere o executável contra o SHA256SUMS da versão, se ele estiver ao lado.
$sums = Join-Path (Split-Path $Agent) "SHA256SUMS"
if (Test-Path $sums) {
  $name = Split-Path $Agent -Leaf
  $line = Get-Content $sums | Where-Object { $_ -match "\s\*?$([regex]::Escape($name))$" } | Select-Object -First 1
  if (-not $line) { Fail "$name não está em SHA256SUMS" }
  $want = ($line -split "\s+")[0].ToLower()
  $got = (Get-FileHash $Agent -Algorithm SHA256).Hash.ToLower()
  if ($want -ne $got) { Fail "o SHA-256 de $name não confere com SHA256SUMS: arquivo alterado ou corrompido" }
  Write-Host "executável conferido: $got"
}

$state = Join-Path $env:ProgramData "Invenire"
$enrolled = Test-Path (Join-Path $state "agent.json")
if ($enrolled -and -not $Force) {
  Write-Host "já cadastrado ($state); pulando o cadastro (-Force cadastra de novo e revoga o anterior)"
} else {
  if (-not $Token) { Fail "falta o token: defina INVENIRE_ENROLL_TOKEN ou passe -Token" }
  $enrollArgs = @("enroll", "--server", $Server)
  if ($CaPin) { $enrollArgs += @("--ca-pin", $CaPin) }
  if ($Force) { $enrollArgs += "--force" }
  $env:INVENIRE_ENROLL_TOKEN = $Token
  & $Agent @enrollArgs
  if ($LASTEXITCODE -ne 0) { Fail "o cadastro falhou (veja a mensagem acima e a tabela 'Quando dá errado' do README)" }
}

& $Agent service install --interval $Interval
if ($LASTEXITCODE -ne 0) { Fail "a instalação do serviço falhou" }

Start-Sleep -Seconds 3
$svc = Get-Service invenire-agent -ErrorAction SilentlyContinue
if (-not $svc -or $svc.Status -ne "Running") { Fail "o serviço invenire-agent não está rodando; veja o Visualizador de Eventos (Aplicativo, origem invenire-agent)" }
Write-Host "pronto: serviço invenire-agent rodando; a primeira coleta já foi enviada ou está na fila" -ForegroundColor Green
