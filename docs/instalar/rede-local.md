Português | [English](../en/install/local-network.md)

# Instalar na rede local

Para quando as máquinas inventariadas e o servidor estão na mesma rede (ou numa VPN) e não há
domínio nem certificado público. O servidor atende HTTPS direto na porta 8443, com um
certificado emitido pela CA da própria instalação; cada agente reconhece o servidor pelo
**pin** dessa CA, que vai dentro do pacote do instalador.

Não é o seu caso? Veja [qual cenário escolher](escolha.md).

Tempo: uns 20 minutos, a maior parte esperando a imagem ser montada.

## O que você precisa

- Uma máquina Linux para o servidor, sempre ligada, com **Docker Engine** e o plugin
  **compose** (`docker compose version` responde) e o **git**.
- O **IP fixo** dessa máquina na rede (ou um nome DNS interno que aponte para ela). As máquinas
  vão chamar o servidor por esse endereço, e ele entra no certificado: um endereço fora da lista
  é recusado pelo agente.
- A porta **8443/tcp** liberada no firewall da máquina do servidor para a rede interna.
- Um navegador numa máquina da rede, para o painel.

Nos exemplos, o servidor é `192.168.0.10` e tem o nome `invenire.local`. Troque pelos seus.

## 1. Baixar o Invenire

Na máquina do servidor:

```sh
sudo git clone https://github.com/invenire-hq/invenire.git /opt/invenire
cd /opt/invenire/deploy/servidor
```

## 2. Configurar

```sh
sudo cp .env.example .env
openssl rand -hex 24
sudo nano .env
```

No `.env`, deixe assim (o resto pode ficar como está):

```ini
COMPOSE_FILE=compose.yml
INVENIRE_PG_PASSWORD=<a senha que o openssl gerou>
INVENIRE_HOSTS=localhost,invenire.local,192.168.0.10
```

`INVENIRE_HOSTS` são **todos** os nomes e IPs pelos quais alguém vai chamar o servidor: as
máquinas e também o navegador do painel. Se depois quiser acrescentar um, mude aqui e suba de
novo (passo 3); o certificado é refeito a cada subida.

Guarde a senha do banco junto com os backups: o `.env` não vai para o git.

## 3. Subir

```sh
sudo docker compose up -d --wait
```

A primeira vez baixa as imagens, entre elas a do servidor
(`ghcr.io/invenire-hq/invenire-server`, que já traz o agente para Windows e Linux). Para
montar a imagem a partir do código, use `--build` (alguns minutos). O PostgreSQL e o servidor
voltam sozinhos quando a máquina reinicia.

**Conferir.** Os dois serviços aparecem `healthy`:

```sh
sudo docker compose ps
```

E o servidor diz que está no ar, com o pin da CA:

```sh
sudo docker compose logs server | grep "no ar"
```

```
server-1  | {"time":"…","level":"INFO","msg":"invenire-server no ar","version":"dev","listen":":8443","tls":"internal","ca_sha256":"5d75…f345"}
```

Da máquina onde vai abrir o navegador, confira que a porta responde (o `-k` é porque o
certificado é da CA da instalação, que o `curl` não conhece):

```sh
curl -k -o /dev/null -w '%{http_code}\n' https://192.168.0.10:8443/healthz
```

Tem de mostrar `204`. Sem resposta, é firewall ou endereço.

## 4. Primeira configuração no painel

Pegue o código da primeira configuração:

```sh
sudo docker compose logs server | grep "primeira configuração"
```

```
server-1  | {"time":"…","level":"WARN","msg":"primeira configuração: abra o painel no navegador (/setup) e use este código","codigo":"WGQS-XWWH-TULK"}
```

Abra no navegador **o mesmo endereço que as máquinas vão usar**, por exemplo
`https://192.168.0.10:8443/`. O painel monta o pacote do instalador com o endereço pelo qual
você o abriu: aberto por `localhost`, o pacote mandaria as máquinas para `localhost`.

O navegador vai avisar que a conexão não é segura: ele não conhece a CA da instalação. É
esperado neste cenário. Clique em **Avançado** e em **Continuar para 192.168.0.10**. Se
aparecer uma janela pedindo para escolher um certificado (comum com e-CPF ou e-CNPJ
instalado), clique em **Cancelar**: o servidor pede certificado só por causa dos agentes.

O endereço leva a **Primeira configuração**. Preencha o código, o nome da organização e o
primeiro administrador (nome, e-mail e senha) e clique em **Criar organização**.

**Conferir.** Você entra logado na tela **Adicionar máquinas**, com "Tudo pronto para
começar". Se o código não for aceito, confira se copiou a linha do log mais recente (cada
subida sem organização gera um código novo).

## 5. Token de cadastro e pacote do instalador

Ainda em **Adicionar máquinas**:

1. (Opcional) Crie uma unidade para as máquinas entrarem já no lugar certo, por exemplo
   "Sede". Dá para fazer depois, em **Unidades**.
2. Em **Novo token de cadastro**, escolha a unidade, **Quantas máquinas** o token cadastra e a
   **Validade**, e clique em **Criar token**.

A tela mostra o token (só desta vez), o **Endereço do servidor** e o **Pin da CA**.

**Conferir.** O endereço é `https://192.168.0.10:8443` (o que as máquinas alcançam) e o pin é o
mesmo `ca_sha256` da linha "no ar" do log (passo 3), com `sha256:` na frente.

Em **1. Pacote para baixar**, baixe o pacote do sistema da máquina:

- **Windows:** `Baixar pacote para Windows (.zip)`;
- **Linux:** `Baixar pacote para Linux (.tar.gz)`.

O pacote leva o agente, o instalador e um `instalar.conf` com endereço, pin e token. Trate-o
como uma senha. O **2. Comando de uma linha** aparece como "Não disponível neste servidor": com
pin, o download pela linha de comando não teria como conferir o servidor.

## 6. Instalar na primeira máquina

Leve o pacote até a máquina (pasta compartilhada, pendrive, `scp`).

**Windows:** botão direito no `.zip` > **Extrair tudo**, entre na pasta `invenire-agente` e dê
dois cliques em `Instalar.cmd`. Responda **Sim** quando o Windows pedir permissão de
administrador. Se aparecer "O Windows protegeu o computador", clique em **Mais informações** e
em **Executar assim mesmo** (os executáveis ainda não têm assinatura de código; veja
[agentes.md](../agentes.md#windows-smartscreen-e-antivírus)).

**Linux** (com systemd, amd64 ou arm64):

```sh
tar xzf invenire-agente-linux.tar.gz
sudo ./invenire-agente/instalar.sh
```

**Conferir.** O instalador termina com:

```
pronto: serviço invenire-agent ativo; a primeira coleta já foi enviada ou está na fila
```

No Windows a janela mostra "Pronto. Pode fechar esta janela.". Se o token era de uma máquina
só, o instalador apaga o `instalar.conf` e avisa.

Na máquina, o serviço está rodando:

```sh
systemctl status invenire-agent          # Linux
```

```powershell
Get-Service invenire-agent               # Windows, no PowerShell
```

## 7. Ver a máquina `em-dia`

Volte ao painel, em **Visão geral**. A máquina aparece em alguns segundos com o estado
`em-dia` (a página se atualiza sozinha a cada minuto; recarregue para ver antes). Em
**Máquinas**, clique nela para ver o software instalado.

Pela linha de comando, no servidor:

```sh
sudo docker compose exec server invenire-server status --tenant "Minha Organização"
```

Troque "Minha Organização" pelo nome que você deu no passo 4. A máquina aparece com
`em-dia`.

Se ela não aparecer, ou aparecer com outro estado, veja [problemas.md](../problemas.md).

## Depois

- **Backup**, desde o primeiro dia: [operacao.md](../operacao.md#backup). Sem o backup da
  pasta de dados (CA e chave-mestra), um servidor perdido obriga a recadastrar todas as máquinas.
- **Mais máquinas**, atualização e desinstalação do agente: [agentes.md](../agentes.md).
- **Outras pessoas no painel**: em **Acesso**, crie usuários (`read` só consulta; `admin`
  também altera).
- **Certificado sem aviso no navegador**: com um domínio, passe para um
  [proxy reverso](proxy-reverso.md). O servidor deixa de atender na porta 8443, e cada agente
  já cadastrado precisa ser apontado para o endereço novo, sem recadastrar: veja
  [agentes.md](../agentes.md#trocar-o-servidor).
