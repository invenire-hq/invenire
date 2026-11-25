Português | [English](../en/install/choosing.md)

# Qual cenário é o seu

O servidor do Invenire (`invenire-server` com PostgreSQL, em Docker) sobe de três jeitos. Todos
usam a pasta `deploy/servidor/` do repositório; muda só o arquivo do compose, escolhido no
`.env` (`COMPOSE_FILE`). Em todos, a primeira configuração e o dia a dia são pelo painel no
navegador, e as máquinas instalam o agente com um pacote baixado do painel.

Responda de cima para baixo e pare na primeira linha que bater:

| Pergunta | Se sim | Roteiro | Arquivo |
|---|---|---|---|
| Já existe um proxy reverso (Nginx, Traefik, Nginx Proxy Manager…) com domínio e certificado público na frente do que vai rodar o Invenire? | ele termina o TLS e repassa ao Invenire | [Proxy reverso](proxy-reverso.md) | `compose.proxy.yml` |
| Tem um domínio que aponta para a máquina, e as portas 80 e 443 dela estão abertas para a internet (uma VPS, por exemplo)? | o Caddy tira o certificado sozinho | [VPS](vps.md) | `compose.caddy.yml` |
| As máquinas estão todas na mesma rede (ou VPN) que o servidor, sem domínio nem certificado público? | o servidor usa a CA da própria instalação, e os agentes o reconhecem pelo pin | [Rede local](rede-local.md) | `compose.yml` |

Na dúvida entre os três, o mais simples para começar é a **rede local**: nada de domínio,
certificado ou porta aberta na internet.

## O que muda entre eles

| | Rede local | VPS | Proxy reverso |
|---|---|---|---|
| Endereço das máquinas | `https://192.168.0.10:8443` | `https://invenire.exemplo.org` | `https://invenire.exemplo.org` |
| Certificado | da CA da instalação | público (Let's Encrypt, pelo Caddy) | público (o do proxy) |
| O navegador avisa sobre o certificado | sim (é esperado) | não | não |
| Como o agente confere o servidor | pelo pin que vai no pacote | pelas CAs do sistema | pelas CAs do sistema |
| Como o servidor confere o agente | certificado cliente (mTLS) ou assinatura | assinatura de cada requisição | assinatura de cada requisição |
| Comando de uma linha para instalar | não (só o pacote) | sim | sim |
| Máquinas fora da rede | só por VPN | sim | sim |
| Portas abertas | 8443 na rede interna | 80 e 443 na internet | as do proxy |

## Perguntas comuns

**Tenho domínio, mas a máquina não fica exposta na internet.** Sem a porta 80 aberta, o
Let's Encrypt não consegue conferir o domínio pelo Caddy. Use a [rede local](rede-local.md),
ou, se já tiver um certificado para o domínio, um [proxy reverso](proxy-reverso.md) com ele.

**Já tenho um Nginx, mas ele não deve ver o conteúdo** (repassa o TLS intacto). É o modo
passthrough, explicado em [proxy-reverso.md](proxy-reverso.md#outros-proxies): o servidor fica
como na rede local, e o proxy só encaminha.

**Dá para trocar de cenário depois?** Dá: os três compartilham os mesmos dados (volumes do
compose `invenire-servidor`). Pare o atual (`docker compose down`, **sem** `-v`), mude o
`COMPOSE_FILE` e suba de novo. Os agentes já cadastrados precisam ser apontados para o endereço
novo, sem recadastrar ([agentes.md](../agentes.md#trocar-o-servidor)).

**E sem Docker?** O servidor é um binário só; veja [operacao.md](../operacao.md#sem-docker).
