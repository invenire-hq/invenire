# Instalar numa VPS

Para quando o servidor fica na internet, numa VPS (ou qualquer máquina com IP público), e as
máquinas inventariadas o alcançam de onde estiverem. Na frente do `invenire-server` sobe um
**Caddy**, que tira e renova sozinho o certificado do Let's Encrypt: basta informar o domínio.
O servidor fala HTTP puro só com o Caddy, pela rede interna do compose, e o agente se
autentica assinando cada requisição.

Já tem um Nginx ou outro proxy nessa máquina? Use o roteiro do
[proxy reverso](proxy-reverso.md). Não sabe qual é o seu caso? Veja [escolha.md](escolha.md).

## O que você precisa

- Uma VPS com Linux (1 vCPU e 1 GB de memória bastam para começar), com **Docker Engine**, o
  plugin **compose** (`docker compose version` responde) e o **git**. Para instalar o Docker,
  siga a documentação oficial do Docker para a sua distribuição.
- Um **domínio** (ou subdomínio) só para o Invenire, com um registro **A** (e **AAAA**, se a VPS
  tiver IPv6) apontando para o IP da VPS. Nos exemplos, `invenire.exemplo.org`.
- As portas **80** e **443/tcp** abertas para a internet, no firewall do provedor e no da VPS.
  A 80 é por onde o Let's Encrypt confere o domínio; o Caddy manda todo o resto para HTTPS.
- Nada mais escutando nas portas 80 e 443 da VPS.

**Conferir antes de começar.** O domínio já aponta para a VPS:

```sh
getent hosts invenire.exemplo.org
```

Tem de mostrar o IP da VPS. Se acabou de criar o registro, pode levar alguns minutos.

## 1. Baixar o Invenire

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

No `.env`:

```ini
COMPOSE_FILE=compose.caddy.yml
INVENIRE_PG_PASSWORD=<a senha que o openssl gerou>
INVENIRE_DOMAIN=invenire.exemplo.org
```

(Tire o `#` da frente de `INVENIRE_DOMAIN`.)

## 3. Subir

```sh
sudo docker compose up -d --wait
```

A primeira vez baixa as imagens (`--build` monta a do servidor a partir do código).
PostgreSQL, servidor e Caddy voltam sozinhos quando a VPS reinicia.

**Conferir.** `sudo docker compose ps` mostra os três serviços de pé (`postgres` e `server`
como `healthy`). O Caddy tira o certificado logo depois de subir:

```sh
sudo docker compose logs caddy | grep "certificate obtained successfully"
```

Se não aparecer em um minuto, veja os erros com `sudo docker compose logs caddy`: quase sempre é
o domínio não apontando para a VPS ou a porta 80 fechada.

De qualquer máquina:

```sh
curl -s -o /dev/null -w '%{http_code}\n' https://invenire.exemplo.org/healthz
```

Tem de mostrar `204`, sem erro de certificado.

## 4. Primeira configuração no painel

Pegue o código:

```sh
sudo docker compose logs server | grep "primeira configuração"
```

Faça isto logo: até a primeira configuração, quem tiver o código pode criar a organização, e
ele só aparece no log da VPS.

Abra `https://invenire.exemplo.org/` no navegador, sem aviso de certificado. Ele leva a
**Primeira configuração**: preencha o código, o nome da organização e o primeiro administrador
e clique em **Criar organização**.

**Conferir.** Você entra logado em **Adicionar máquinas**, com "Tudo pronto para começar".

## 5. Token de cadastro

Em **Adicionar máquinas**, crie (se quiser) uma unidade, e em **Novo token de cadastro**
escolha a unidade, **Quantas máquinas** e a **Validade** e clique em **Criar token**.

**Conferir.** O **Endereço do servidor** é `https://invenire.exemplo.org` e o **Pin da CA** diz
"nenhum: o servidor usa certificado público".

## 6. Instalar na primeira máquina

**Comando de uma linha** (em **2. Comando de uma linha**, na tela do token). No Linux:

```sh
curl -fsSL https://invenire.exemplo.org/download/instalar.sh | sudo INVENIRE_ENROLL_TOKEN=inv_… sh
```

No Windows, o comando da tela, no PowerShell **como administrador**.

Ou o **pacote para baixar**, como na [rede local](rede-local.md#6-instalar-na-primeira-máquina):
`.zip` com `Instalar.cmd` no Windows, `.tar.gz` com `instalar.sh` no Linux.

**Conferir.** O instalador termina com
`pronto: serviço invenire-agent ativo; a primeira coleta já foi enviada ou está na fila`.

## 7. Ver a máquina `em-dia`

No painel, em **Visão geral**, a máquina aparece em alguns segundos com `em-dia`. Pela linha de
comando, na pasta `deploy/servidor`:

```sh
sudo docker compose exec server invenire-server status --tenant "Minha Organização"
```

Se não aparecer, veja [problemas.md](../problemas.md).

## Depois

- **Backup**, para fora da VPS: [operacao.md](../operacao.md#backup). O certificado do Caddy
  não entra no backup nem precisa: numa VPS nova, o Caddy tira outro sozinho.
- **Mais máquinas**, atualização e desinstalação do agente: [agentes.md](../agentes.md).
- O painel fica exposto na internet: use senhas fortes e dê `admin` só a quem precisa
  ([seguranca.md](../seguranca.md)).

## Como o Caddy está configurado

`deploy/servidor/caddy/Caddyfile`:

```caddy
{$INVENIRE_DOMAIN} {
	request_body {
		max_size 32MB
	}
	reverse_proxy server:8080 {
		transport http {
			read_timeout 5m
			write_timeout 5m
		}
	}
}
```

O Caddy já faz o que o Nginx precisa de várias linhas para fazer: HTTP para HTTPS, certificado
e renovação, TLS 1.3, `Host`, `X-Forwarded-For` (descartando o que o cliente mandou) e
`X-Forwarded-Proto`. O servidor aceita os cabeçalhos dele porque o Caddy conecta de dentro da
rede do compose (`--trusted-proxies 172.31.250.0/24`). O limite de corpo e os tempos são os
mesmos da [receita do Nginx](proxy-reverso.md#5-nginx).
