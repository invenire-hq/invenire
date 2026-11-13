@echo off
rem Instalador do agente do Invenire para Windows: depois de extrair o .zip
rem inteiro, dois cliques aqui. Pede permissão de administrador (UAC) e roda o
rem instalar.ps1 desta pasta, que lê o endereço, o pin e o token do
rem instalar.conf ao lado.
chcp 65001 >nul
setlocal
title Instalador do Invenire

if not exist "%~dp0instalar.ps1" goto :semscript

rem Sem permissão de administrador: abre de novo pedindo (a janela do UAC).
net session >nul 2>&1
if errorlevel 1 goto :elevar

echo Instalando o agente do Invenire...
echo.
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0instalar.ps1"
set "rc=%errorlevel%"
echo.
if "%rc%"=="0" (
  echo Pronto. Pode fechar esta janela.
) else (
  echo A instalação não terminou. Leia a mensagem acima para saber o que fazer.
)
echo.
pause
exit /b %rc%

:elevar
echo Pedindo permissão de administrador...
set "INVENIRE_CMD=%~f0"
powershell.exe -NoProfile -Command "try { Start-Process -FilePath $env:INVENIRE_CMD -Verb RunAs -ErrorAction Stop } catch { exit 1 }"
if errorlevel 1 (
  echo.
  echo A permissão de administrador não foi dada, então nada foi instalado.
  echo Dê dois cliques no Instalar.cmd de novo e responda "Sim" na janela que aparecer.
  echo Se a sua conta não for de administrador, peça a quem cuida dos computadores.
  echo.
  pause
  exit /b 1
)
exit /b 0

:semscript
echo.
echo O arquivo instalar.ps1 não está ao lado do Instalar.cmd.
echo Provavelmente o .zip foi aberto sem extrair. Feche esta janela, clique com o
echo botão direito no arquivo .zip, escolha "Extrair tudo" e dê dois cliques no
echo Instalar.cmd que fica na pasta extraída.
echo.
pause
exit /b 1
