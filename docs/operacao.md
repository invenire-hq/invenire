# Operação do servidor

O dia a dia de quem cuida do servidor instalado por um dos [roteiros](instalar/escolha.md):
backup, restauração, atualização, monitoramento e a linha de comando de administração. Os
comandos rodam na pasta `deploy/servidor`, onde o `.env` diz qual compose está em uso
(`COMPOSE_FILE`); assim nenhum precisa de `-f`.

## Linha de comando de administração

Quase tudo se faz pelo painel. O que não se faz (ou para scripts) roda dentro do contêiner:

```sh
cd /opt/invenire/deploy/servidor
sudo docker compose exec server invenire-server help
```

Para não repetir o prefixo, uma função no shell (ponha no `~/.bashrc` para valer sempre):

```sh
inv() { (cd /opt/invenire/deploy/servidor && sudo docker compose exec server invenire-server "$@"); }
inv status --tenant "Minha Organização"
```

| Comando | Para quê |
|---|---|
| `status --tenant <org> [--stale 12h] [--check]` | o parque, uma máquina por linha, as que pedem atenção primeiro |
| `user set-password --email <e-mail>` | troca a senha de login do painel (quem esqueceu a senha) e encerra as sessões; a senha vem de `INVENIRE_PASSWORD` ou é gerada e mostrada |
| `tenant create [--admin-email <e-mail> --admin-name <nome>] <org>` | cria outra organização (a primeira é criada pelo `/setup`) |
| `token create --tenant <org> [--unit <caminho>] [--uses N] [--ttl 24h] [--description …]` | token de cadastro, como em **Adicionar máquinas** |
| `token list --tenant <org> [--all]` / `token revoke --tenant <org> <id>` | tokens de cadastro e quantos usos restam; revogar um que vazou |
| `unit create --tenant <org> [--parent <caminho>] [--kind <tipo>] <nome>` / `unit list --tenant <org>` | unidades |
| `actor create --tenant <org> --name <nome> --scopes read,admin [--kind human\|ai_agent\|ci] [--ttl …]` | usuário ou pipeline com token de API (`inva_…`) |
| `tenant export --tenant <org> --out <arquivo> [--with-ca-key]` / `tenant verify <arquivo>` | exportação da organização ([exportacao.md](exportacao.md)) |
| `health [--url …]` | confere o `/healthz` local (é o healthcheck da imagem) |
| `version` | versão do servidor |

## Backup

```sh
cd /opt/invenire/deploy/servidor
sudo ./backup.sh /var/backups/invenire
```

Grava, numa pasta com data e hora, o dump do banco (`invenire.dump`) e os dados do servidor
(`data/`: a CA da instalação e a chave-mestra), com um `SHA256SUMS`, e mantém os 14 mais novos.
**Um sem o outro não restaura:** sem a chave-mestra o servidor não emite crachás novos; sem a CA
da instalação os agentes com pin deixam de reconhecer o servidor.

Para todo dia, no cron do root (`sudo crontab -e`):

```
0 2 * * * cd /opt/invenire/deploy/servidor && ./backup.sh /var/backups/invenire
```

Copie a pasta de backups para **fora** da máquina (outro servidor, disco externo, armazenamento
na nuvem). Guarde também a senha do banco (`INVENIRE_PG_PASSWORD`, no `.env`). O certificado do
Caddy (cenário VPS) não entra no backup e não precisa: numa máquina nova, o Caddy tira outro.

**Conferir:** o script termina com `backup em /var/backups/invenire/<data>`, e a pasta tem
`invenire.dump`, `data/master.key`, `data/ca/` e `SHA256SUMS`.

## Restauração

Numa máquina nova (ou com os volumes vazios), com o repositório em `/opt/invenire` e o `.env`
preenchido (o mesmo `COMPOSE_FILE`; a senha do banco pode ser outra, o dump não a carrega):

```sh
cd /opt/invenire/deploy/servidor
sudo ./restaurar.sh /var/backups/invenire/20261110-161950
```

O script confere o backup contra o `SHA256SUMS`, restaura o banco, põe os dados do servidor no
volume com o dono certo e sobe tudo. **Apaga** o que houver no banco e nos dados do servidor.

Restaurado com a mesma CA e no mesmo endereço, os agentes continuam sem nada mudar neles. Se o
endereço mudou, aponte cada agente para o novo ([agentes.md](agentes.md#trocar-o-servidor)).

**Conferir:** `sudo docker compose exec server invenire-server status --tenant "Minha
Organização"` mostra as máquinas; na próxima coleta de cada uma (ou reiniciando o serviço do
agente) ela volta a `em-dia`.

Testado: backup, `docker compose down -v` (apaga os volumes), restauração e o agente voltando
a `em-dia` sem mudança nenhuma nele.

Uma cópia portátil de uma organização, para guardar ou levar a outro servidor, é o
`tenant export` ([exportacao.md](exportacao.md)).

## Atualizar o servidor

```sh
cd /opt/invenire
sudo git pull
cd deploy/servidor
sudo ./backup.sh /var/backups/invenire
sudo docker compose pull
sudo docker compose up -d --wait
```

Com `INVENIRE_VERSION` fixa no `.env`, mude-a para a versão nova antes do `pull` (as versões
estão no [CHANGELOG](../CHANGELOG.md) e nas releases do repositório). Quem monta a imagem
a partir do código troca o `pull` por `up -d --build --wait`.

O servidor aplica as migrações do banco ao subir. A imagem nova traz também o agente novo: as
máquinas o recebem quando o instalador roda de novo ([agentes.md](agentes.md#atualizar)).

## Monitorar

- **Painel:** **Visão geral** mostra as máquinas que pedem atenção, com o que fazer em cada
  estado ([problemas.md](problemas.md#estados-das-máquinas)).
- **Cron:** `status --check` sai com código 1 se alguma máquina pedir atenção:

  ```
  0 8 * * * cd /opt/invenire/deploy/servidor && docker compose exec -T server invenire-server status --tenant "Minha Organização" --check > /dev/null || <avisar>
  ```

- **Saúde do serviço:** `GET /healthz` responde `204` quando o servidor e o banco respondem
  (pelo endereço público, ou `docker compose ps`, que mostra o healthcheck).
- **Logs:** `sudo docker compose logs server` (JSON, uma linha por evento). Erros de ingestão
  aparecem também no fim do `status` e em **Visão geral** > **Erros recentes**.

## Dados e chaves

Na primeira subida o servidor cria, no volume `data` (`/var/lib/invenire/data` no contêiner):

- `ca/`: a CA da instalação, que emite o certificado TLS do servidor no modo direto
  (`compose.yml`), para os nomes de `INVENIRE_HOSTS`. Atrás de proxy ela não é criada;
- `master.key`: a chave-mestra, que cifra (AES-256-GCM) a chave da CA de cada organização,
  guardada no banco. Pode vir de `INVENIRE_MASTER_KEY` (32 bytes em base64) em vez do arquivo.

O código da primeira configuração e o limite de tentativas de login ficam na memória do
processo: com mais de uma réplica do servidor, cada uma teria o seu. Rode uma só.

## Sem Docker

O servidor é um binário só (`invenire-server`), que precisa de um PostgreSQL 17 e da URL dele em
`INVENIRE_DATABASE_URL`. Montado como em [desenvolvimento.md](desenvolvimento.md), sobe com:

```sh
export INVENIRE_DATABASE_URL="postgres://invenire:SENHA@localhost:5432/invenire?sslmode=disable"
invenire-server serve --listen :8443 --data /var/lib/invenire/data --hosts invenire.local,192.168.0.10 --agent-dir /opt/invenire/dist/0.1.0
```

Opções do `serve`:

| Opção | Padrão | Faz |
|---|---|---|
| `--listen` | `:8443` | endereço de escuta |
| `--data` | `data` | pasta da CA da instalação e da chave-mestra |
| `--hosts` | `localhost,127.0.0.1` | nomes e IPs do certificado do servidor (modo direto) |
| `--tls-cert`, `--tls-key` | | certificado público (PEM com a cadeia) no lugar do da CA da instalação; relido sozinho quando o arquivo muda (renovação do certbot) |
| `--behind-proxy` | | HTTP puro atrás de um proxy que termina o TLS ([proxy-reverso.md](instalar/proxy-reverso.md)) |
| `--trusted-proxies` | | com `--behind-proxy`, obrigatório: IPs ou CIDRs de onde o proxy conecta |
| `--agent-dir` | `INVENIRE_AGENT_DIR`, ou a pasta da imagem | executáveis do agente e `SHA256SUMS` para `/download/` e o pacote do instalador (a pasta de `scripts/build.sh` serve) |
| `--stale` | `12h` | no painel, sem notícia do agente por mais que isto = `atrasado` |
| `--auto-map-confidence` | `0.9` | confiança mínima para uma regra mapear software sem revisão |

Sem os executáveis do agente, o servidor sobe assim mesmo, avisa no log, e o painel explica que
não pode montar o pacote.
