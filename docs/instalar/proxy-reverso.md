# Instalar atrás de um proxy reverso

Para quando já existe (ou vai existir) um proxy reverso na frente, com um domínio e um
certificado público: o Nginx da organização, um Nginx Proxy Manager, um Traefik. O proxy
termina o TLS e repassa ao `invenire-server` em HTTP puro, numa porta que só ele alcança. O
agente prova quem é **assinando cada requisição** com a chave do crachá, então o proxy não
precisa repassar certificado cliente nem ter configuração especial.

A receita abaixo é a do **Nginx** com certificado do Let's Encrypt (certbot), na mesma máquina
do servidor. Para outros proxies, veja a [matriz de compatibilidade](#outros-proxies). Sem
proxy nenhum e com uma VPS nova, o caminho mais curto é o da [VPS com Caddy](vps.md).

Não é o seu caso? Veja [qual cenário escolher](escolha.md).

## O que você precisa

- Uma máquina Linux com **Docker Engine** e o plugin **compose** (`docker compose version`
  responde), o **git** e o **Nginx**.
- Um **domínio** (ou subdomínio) só para o Invenire, apontando para a máquina do Nginx. Nos
  exemplos, `invenire.exemplo.org`. Num subcaminho (`exemplo.org/invenire/`) não funciona: o
  painel e os instaladores usam caminhos a partir da raiz.
- As portas **80** e **443** abertas para quem vai usar: as máquinas inventariadas e o navegador
  do painel (e, para o certbot, a internet).

## 1. Baixar o Invenire

```sh
sudo git clone <endereço do repositório> /opt/invenire
cd /opt/invenire/deploy/servidor
```

> O endereço público do repositório ainda vai ser definido. Enquanto isso, copie a pasta do
> repositório para `/opt/invenire` como preferir.

## 2. Configurar

```sh
sudo cp .env.example .env
openssl rand -hex 24
sudo nano .env
```

No `.env`:

```ini
COMPOSE_FILE=compose.proxy.yml
INVENIRE_PG_PASSWORD=<a senha que o openssl gerou>
```

Com o Nginx nesta mesma máquina, é só isso: o servidor fica em `127.0.0.1:8080`, e só o que
chega por essa porta é tratado como vindo do proxy. Com o proxy **em outra máquina**, veja
[Proxy em outra máquina](#proxy-em-outra-máquina).

## 3. Subir o servidor

```sh
sudo docker compose up -d --build --wait
```

A primeira vez monta a imagem (alguns minutos). O PostgreSQL e o servidor voltam sozinhos
quando a máquina reinicia.

**Conferir.** Os dois serviços aparecem `healthy` em `sudo docker compose ps`, e:

```sh
sudo docker compose logs server | grep "no ar"
```

```
server-1  | {"time":"…","level":"INFO","msg":"invenire-server no ar, em HTTP atrás de proxy","version":"dev","listen":":8080","tls":"proxy","trusted_proxies":"172.31.250.0/24"}
```

E a porta responde só nesta máquina:

```sh
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/healthz
```

Tem de mostrar `204`.

## 4. Certificado

Se o Nginx já tem um certificado para o domínio, pule para o passo 5 e use os caminhos dele.
Senão, com o domínio já apontando para esta máquina e a porta 80 aberta (Debian e Ubuntu):

```sh
sudo apt install nginx certbot python3-certbot-nginx
sudo certbot certonly --nginx -d invenire.exemplo.org
```

O certificado fica em `/etc/letsencrypt/live/invenire.exemplo.org/` e o certbot o renova
sozinho. Para o Nginx passar a usar o renovado:

```sh
printf '#!/bin/sh\nsystemctl reload nginx\n' | sudo tee /etc/letsencrypt/renewal-hooks/deploy/reload-nginx
sudo chmod +x /etc/letsencrypt/renewal-hooks/deploy/reload-nginx
sudo certbot renew --dry-run
```

## 5. Nginx

```sh
sudo cp /opt/invenire/deploy/servidor/nginx/invenire.conf /etc/nginx/conf.d/invenire.conf
sudo sed -i 's/invenire.exemplo.org/SEU.DOMINIO/g' /etc/nginx/conf.d/invenire.conf
sudo nginx -t && sudo systemctl reload nginx
```

(No Debian e no Ubuntu, `conf.d/` já é lida pelo `nginx.conf`. Em outras distribuições, use a
pasta que o `nginx.conf` inclui, como `http.d/` no Alpine.)

O arquivo inteiro, com o porquê de cada linha:

```nginx
server {
    listen 80;
    listen [::]:80;
    server_name invenire.exemplo.org;
    # Nada do Invenire passa em HTTP: o token de cadastro iria em claro.
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;    # Nginx 1.25.1+; antes disso: "listen 443 ssl http2;" e sem esta linha
    server_name invenire.exemplo.org;

    ssl_certificate     /etc/letsencrypt/live/invenire.exemplo.org/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/invenire.exemplo.org/privkey.pem;
    # O agente exige TLS 1.3; o 1.2 fica para navegadores antigos.
    ssl_protocols TLSv1.2 TLSv1.3;

    # Snapshot em gzip pode chegar a 32 MiB (o limite do servidor).
    client_max_body_size 32m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;

        proxy_set_header Host              $host;
        # $remote_addr, e não $proxy_add_x_forwarded_for: descarta o que o
        # cliente tenha mandado nesse cabeçalho.
        proxy_set_header X-Forwarded-For   $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Host  $host;

        # Snapshots grandes em links lentos.
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
    }
}
```

| Linha | Sem ela |
|---|---|
| `return 301 https://…` no `listen 80` | o painel e o cadastro funcionariam em HTTP, com senha e token em claro; o servidor recusa com 403 o que o proxy disser que chegou por HTTP |
| `ssl_protocols … TLSv1.3` | o agente não conecta (ele só aceita TLS 1.3) |
| `client_max_body_size 32m` | o padrão do Nginx é 1 MiB: máquinas com muito software recebem `413`; o inventário fica na fila do agente e não chega enquanto o limite não subir |
| `proxy_set_header Host $host` | o painel monta o endereço dos instaladores com o `Host`: sem ele, os pacotes mandariam as máquinas para `127.0.0.1:8080` |
| `X-Forwarded-For $remote_addr` | os logs e o limite de tentativas de login veriam todo mundo com o IP do proxy |
| `X-Forwarded-Proto $scheme` | sem problema de segurança, mas o servidor perde a confirmação de que a requisição chegou por HTTPS |
| `proxy_read_timeout`/`proxy_send_timeout 300s` | snapshot grande num link lento pode cair em 60 s (o padrão) e ser reenviado depois |

O Nginx **não** precisa de `ssl_verify_client` nem de repassar certificado cliente: o agente se
autentica pela assinatura. Assim o navegador de quem abre o painel não mostra seletor de
certificado.

**Conferir**, de qualquer máquina:

```sh
curl -s -o /dev/null -w '%{http_code}\n' https://invenire.exemplo.org/healthz
curl -s -o /dev/null -w '%{http_code} %{redirect_url}\n' http://invenire.exemplo.org/
```

O primeiro mostra `204`, sem erro de certificado; o segundo, `301 https://invenire.exemplo.org/`.
Um `502` é o Nginx sem alcançar o servidor (passo 3); um `403` com "fora de --trusted-proxies"
é o proxy conectando de um endereço que o servidor não conhece (veja
[Proxy em outra máquina](#proxy-em-outra-máquina)).

## 6. Primeira configuração no painel

Pegue o código:

```sh
sudo docker compose logs server | grep "primeira configuração"
```

Abra `https://invenire.exemplo.org/` no navegador, sem aviso de certificado. Ele leva a
**Primeira configuração**: preencha o código, o nome da organização e o primeiro administrador
e clique em **Criar organização**.

**Conferir.** Você entra logado em **Adicionar máquinas**, com "Tudo pronto para começar".

## 7. Token de cadastro

Em **Adicionar máquinas**, crie (se quiser) uma unidade, e em **Novo token de cadastro**
escolha a unidade, **Quantas máquinas** e a **Validade** e clique em **Criar token**.

**Conferir.** O **Endereço do servidor** é `https://invenire.exemplo.org` e o **Pin da CA**
diz "nenhum: o servidor usa certificado público". Se o endereço vier com `127.0.0.1` ou com a
porta 8080, o Nginx não está repassando o `Host`.

## 8. Instalar na primeira máquina

Com certificado público há duas formas sem digitar nada além de colar:

**Comando de uma linha** (em **2. Comando de uma linha**, na tela do token). No Linux:

```sh
curl -fsSL https://invenire.exemplo.org/download/instalar.sh | sudo INVENIRE_ENROLL_TOKEN=inv_… sh
```

No Windows, o comando da tela, no PowerShell **como administrador**. O token vai numa
variável de ambiente, nunca na URL (que ficaria no log do proxy).

**Pacote para baixar**, como na [rede local](rede-local.md#6-instalar-na-primeira-máquina):
`.zip` com `Instalar.cmd` no Windows, `.tar.gz` com `instalar.sh` no Linux.

**Conferir.** O instalador termina com
`pronto: serviço invenire-agent ativo; a primeira coleta já foi enviada ou está na fila`.

## 9. Ver a máquina `em-dia`

No painel, em **Visão geral**, a máquina aparece em alguns segundos com `em-dia`. Pela linha de
comando, na pasta `deploy/servidor`:

```sh
sudo docker compose exec server invenire-server status --tenant "Minha Organização"
```

Se não aparecer, veja [problemas.md](../problemas.md#proxy-reverso).

## Depois

- **Backup**: [operacao.md](../operacao.md#backup).
- **Mais máquinas**, atualização e desinstalação do agente: [agentes.md](../agentes.md).

## Proxy em outra máquina

Quando o Nginx fica numa máquina e o Invenire em outra, a porta HTTP do servidor precisa ser
publicada no IP da rede local, e o servidor precisa saber de onde o proxy conecta. No `.env` da
máquina do Invenire (o proxy é `192.168.0.2`, o Invenire `192.168.0.10`):

```ini
INVENIRE_BIND=192.168.0.10
INVENIRE_TRUSTED_PROXIES=192.168.0.2/32
```

No Nginx, `proxy_pass http://192.168.0.10:8080;`. Feche a porta 8080 no firewall para tudo que
não seja o proxy: por ela o tráfego é HTTP puro.

`--trusted-proxies` (aqui, `INVENIRE_TRUSTED_PROXIES`) é obrigatório no modo proxy: só de
conexões vindas dessas faixas os cabeçalhos `X-Forwarded-*` são lidos. De qualquer outra, uma
requisição que traga `X-Forwarded-For`, `X-Forwarded-Proto` ou `Forwarded` recebe `403`, para
ninguém forjar o próprio endereço. Com o proxy na mesma máquina, o padrão (`172.31.250.0/24`, a
rede do compose) já cobre: a conexão do Nginx ao `127.0.0.1:8080` chega ao contêiner vinda do
gateway dessa rede.

## Outros proxies

Há duas formas de pôr um proxy na frente do Invenire:

- **Terminando o TLS** (o caso acima): o proxy tem o certificado público, vê o conteúdo das
  requisições e repassa em HTTP ao servidor, que sobe com `--behind-proxy`
  (`compose.proxy.yml` ou `compose.caddy.yml`). O agente prova quem é pela assinatura de cada
  requisição, que passa intacta por qualquer proxy HTTP. É a forma recomendada.
- **Repassando o TLS intacto** (passthrough): o proxy só olha o nome pedido (SNI) e repassa os
  bytes cifrados ao servidor, que continua no modo direto (`compose.yml`, porta 8443, CA da
  instalação). O proxy não vê nada; os agentes cadastram **com pin**, e o navegador avisa sobre
  o certificado, como na [rede local](rede-local.md). Serve para dividir a porta 443 com outros
  sites sem dar o TLS ao proxy. O nome público precisa estar em `INVENIRE_HOSTS`. O servidor
  passa a ver todas as conexões vindas do IP do proxy (logs e limite de tentativas de login),
  e não entende o protocolo PROXY: não o ligue.

| Proxy | Terminando TLS (`--behind-proxy`) | Repassando TLS intacto (passthrough) |
|---|---|---|
| **Nginx** | **sim, testado** (receita acima) | **sim, testado**, com `stream` + `ssl_preread` ([exemplo](#nginx-em-passthrough)) |
| **Caddy** | **sim, testado** ([`compose.caddy.yml`](vps.md)) | só com o plugin layer4; não testado |
| Nginx Proxy Manager | sim, um "Proxy Host" comum; não testado | pela aba "Streams" (sem o Let's Encrypt do NPM); não testado |
| Traefik | sim, por labels; não testado | sim, `HostSNI` + `tls.passthrough`; não testado |
| HAProxy | sim (`mode http`); não testado | sim (`mode tcp`); não testado |
| Apache (`mod_proxy`) | sim; não testado | não: o Apache não repassa TLS sem abrir |
| Cloudflare Tunnel / proxy da Cloudflare | sim, por causa da assinatura; não testado | não: a Cloudflare sempre abre o TLS |

"Não testado" quer dizer que a configuração segue o que o proxy documenta e o que o servidor
exige (abaixo), mas ninguém a rodou de ponta a ponta com o Invenire ainda. Se você rodar,
conte como foi.

Qualquer proxy que termine o TLS serve se:

1. aceitar **TLS 1.3** do lado do cliente (o agente não aceita menos);
2. aceitar corpo de até **32 MiB** e **não mexer no corpo** (a assinatura cobre o SHA-256 do
   corpo como o agente enviou, com gzip; um proxy que descomprima, recomprima ou reescreva o
   corpo faz o servidor recusar com `401` "assinatura inválida");
3. repassar o **`Host`** original e pôr **`X-Forwarded-For`** e **`X-Forwarded-Proto: https`**;
4. conectar ao servidor de um endereço listado em `--trusted-proxies`;
5. publicar o Invenire na **raiz** de um domínio, não num subcaminho;
6. **não** pedir certificado cliente nem exigir login próprio na frente (o agente não saberia
   responder).

**O que não funciona**, e por quê:

- **Agente antigo atrás de proxy que termina TLS.** Agentes anteriores à autenticação por
  assinatura só se identificam pelo certificado cliente no TLS, que o proxy não repassa. Eles
  recebem `401`. Atualize o agente antes de pôr o proxy na frente, ou use passthrough.
- **Proxy que corta corpo grande** (o `client_max_body_size` padrão do Nginx é 1 MiB): `413`. O
  inventário fica na fila do agente, que tenta de novo com espera crescente; as máquinas com mais
  software ficam `sem-coleta` ou `atrasado` até o limite subir. Como a fila sai do mais antigo
  para o mais novo, os inventários seguintes esperam atrás dele (a fila guarda até 100). Só o
  `413` que o próprio servidor marca como seu (corpo acima de 32 MiB) é definitivo e vai para
  `queue/rejected/`.
- **Proxy que reescreve o corpo** (alguns WAFs, compressão "otimizada"): `401` "assinatura
  inválida".

Nenhum desses `401` para o agente. Ele só se dá por revogado quando o servidor diz que recusou o
crachá em si (agente revogado ou desconhecido, crachá vencido; a resposta traz um código para
isso). Qualquer outro `401`, do servidor ou de uma página do proxy, é tratado como falha
passageira: o log diz `envio falhou; snapshot fica na fila` com "não é revogação", os
inventários esperam na fila, e o envio volta sozinho quando o proxy for corrigido, sem reiniciar
o serviço nem recadastrar. Uma recusa por relógio errado também não para o agente: ele acerta o
horário pela resposta do servidor e tenta de novo.

Agentes anteriores a esta regra tomam qualquer `401` por revogação: o serviço para (código de
saída 3) e não reinicia sozinho. Depois de corrigir o proxy, reinicie o serviço nessas máquinas
(`systemctl restart invenire-agent` ou `Restart-Service invenire-agent`); não precisa
recadastrar. Eles também mandam para `queue/rejected/` o inventário recusado com `413`.
- **Passthrough com proxy que abre o TLS** (Apache, Cloudflare): não há passthrough; use o modo
  terminando TLS.

### Nginx em passthrough

Testado com Nginx 1.26 (Alpine). O servidor sobe com `compose.yml` (modo direto), e no `.env`:

```ini
COMPOSE_FILE=compose.yml
INVENIRE_HOSTS=invenire.exemplo.org
INVENIRE_PORT=127.0.0.1:8443
```

A porta 8443 fica só em `127.0.0.1`: de fora, só pelo Nginx. No `nginx.conf`, **fora** do
bloco `http` (exige o módulo `stream`; no Debian e no Ubuntu ele vem no pacote
`libnginx-mod-stream`). Se a sua distribuição já traz um bloco `stream` (o Alpine traz, em
`conf.d/stream.conf`, incluindo `stream.d/*.conf`), ponha só o `map` e o `server` dentro dele:

```nginx
stream {
    map $ssl_preread_server_name $invenire_destino {
        invenire.exemplo.org  127.0.0.1:8443;
        default               127.0.0.1:8444;   # os outros sites, num server do http em 8444
    }
    server {
        listen 443;
        ssl_preread on;
        proxy_pass $invenire_destino;
    }
}
```

Os outros sites do Nginx passam a escutar em `127.0.0.1:8444` (com `ssl`), já que a 443 é do
`stream`. Os agentes cadastram com o pin, pelo endereço `https://invenire.exemplo.org`.
