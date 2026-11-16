# Invenire

Inventário de máquinas: o que existe, onde está e de que é feito. Um agente leve em cada máquina
(Windows e Linux) coleta hardware, sistema e software instalado e envia a um servidor, que guarda
o histórico, identifica os produtos e mostra tudo num painel web e numa API.

- **Servidor** (`invenire-server`): PostgreSQL, API em `/v1` e painel web na mesma porta, em
  Docker. Primeira configuração e dia a dia pelo navegador.
- **Agente** (`invenire-agent`): serviço do Windows ou do systemd. Instala com dois cliques num
  pacote baixado do painel, ou com um comando de uma linha.
- **Três jeitos de subir o servidor**: rede local (sem domínio), VPS (Caddy com Let's Encrypt) e
  atrás de um proxy reverso que já existe (Nginx e outros).
- Várias organizações por servidor, unidades em árvore, etiquetas, histórico de instalações e
  remoções, exportação completa da organização com os bytes originais de cada inventário.

## Início rápido (rede local)

Numa máquina Linux com Docker e o plugin compose, que as outras máquinas alcancem na porta 8443:

```sh
sudo git clone <endereço do repositório> /opt/invenire
cd /opt/invenire/deploy/servidor
sudo cp .env.example .env
sudo nano .env      # INVENIRE_PG_PASSWORD (openssl rand -hex 24) e INVENIRE_HOSTS (o IP desta máquina)
sudo docker compose up -d --build --wait
sudo docker compose logs server | grep "primeira configuração"
```

Abra `https://<IP desta máquina>:8443/` no navegador, aceite o aviso de certificado (é da CA da
própria instalação), informe o código do log, crie a organização e o primeiro administrador. Em
**Adicionar máquinas**, crie um token e baixe o pacote do instalador; na máquina, extraia e rode
o `Instalar.cmd` (Windows) ou `sudo ./invenire-agente/instalar.sh` (Linux). Em alguns segundos
ela aparece `em-dia` na **Visão geral**.

O passo a passo completo, com como conferir cada etapa: [docs/instalar/rede-local.md](docs/instalar/rede-local.md).

> O endereço público do repositório ainda vai ser definido.

## Documentação

- [Qual cenário é o seu](docs/instalar/escolha.md): [rede local](docs/instalar/rede-local.md),
  [VPS](docs/instalar/vps.md) ou [proxy reverso](docs/instalar/proxy-reverso.md)
- [Agentes](docs/agentes.md), [painel](docs/painel.md), [operação](docs/operacao.md) (backup,
  restauração, atualização) e [quando dá errado](docs/problemas.md)
- [Segurança](docs/seguranca.md), [API](docs/api.md),
  [como funciona por dentro](docs/funcionamento.md) e [exportação](docs/exportacao.md)
- [Desenvolvimento](docs/desenvolvimento.md): build, testes, sqlc
- [Índice de toda a documentação](docs/README.md)

A arquitetura e o modelo de dados estão no documento
[Invenire — Arquitetura e Modelo de Dados](https://claude.ai/artifact/WfzRB6TWaCNPQxdpx9QJtc).
O `pkg/sysinfo` (leitura de host, SO e software) não depende do formato do snapshot, para poder
ser compartilhado com o [PeritiaGo](https://github.com/athosbes/PeritiaGo).
