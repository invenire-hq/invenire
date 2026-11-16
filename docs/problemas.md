# Quando dá errado

Procure a mensagem (ou o sintoma) na tabela da sua etapa. Se não estiver aqui, abra um issue com
a mensagem inteira, o sistema da máquina e a versão do agente (`invenire-agent version`) e do
servidor (`invenire-server version`).

## Estados das máquinas

O que aparece em **Visão geral** e no `invenire-server status`, com as que pedem atenção
primeiro:

| Estado | Quer dizer | O que fazer |
|---|---|---|
| `em-dia` | o agente apareceu dentro do limite e tudo foi ingerido | nada |
| `na-fila` | inventários esperando a ingestão | normal por alguns segundos; se persistir, veja os logs do servidor |
| `sem-coleta` | cadastrada, mas nenhum inventário chegou | o serviço não subiu ou não alcança o servidor: veja o log do agente ([agentes.md](agentes.md#conferir)) |
| `atrasado` | o agente não aparece há mais de 12h (`--stale`) | máquina desligada, sem rede, ou serviço parado |
| `erro` | algum inventário não pôde ser ingerido | o motivo aparece no fim do `status` e em **Erros recentes**; abra um issue com ele |
| `sem-agente` | o agente foi revogado (ou a máquina foi cadastrada de novo) | recadastre se a máquina ainda é sua |

"Corrente quebrada" ao lado de uma máquina quer dizer que um inventário não continuava o
anterior: relógio mudado, restauração de imagem, ou duas máquinas clonadas com o mesmo
identificador (as duas se revezam no mesmo cadastro, e cada cadastro revoga o anterior).

## Instalação do agente

| Mensagem | Causa | O que fazer |
|---|---|---|
| `certificate is valid for X, not Y` | o servidor (modo direto) subiu sem o nome Y em `INVENIRE_HOSTS` | acrescente Y em `INVENIRE_HOSTS` no `.env`, `docker compose up -d`, e rode o instalador de novo |
| `pin da CA inválido` | o valor de `--ca-pin` não é o pin (talvez o token no lugar dele) | use o pacote do painel, ou copie o pin inteiro, com `sha256:` |
| `a CA do servidor não confere com o pin` | o pin é de outro servidor, ou os dados do servidor foram perdidos e a CA é outra | gere o pacote de novo no painel do servidor certo |
| `servidor respondeu 401: token de cadastro inválido` | token já gasto, vencido, revogado ou incompleto | crie outro token no painel |
| `rode como administrador` / `precisa rodar como root` | terminal sem elevação | Windows: dois cliques no `Instalar.cmd` e **Sim** no UAC; Linux: `sudo` |
| `o agente não está cadastrado` | `service install` antes do `enroll` | use o instalador, ou faça o `enroll` antes |
| `este sistema não usa systemd` | Linux sem systemd | o instalador só cria serviço no systemd; rode `invenire-agent run` por outro supervisor |
| `o programa do agente não está na pasta` (com pin) | script da pasta da versão sem o executável, servidor com pin | use o pacote do painel, que traz o executável |
| `está diferente do que o servidor publicou` | executável corrompido ou alterado | baixe o pacote de novo |
| "O Windows protegeu o computador" | SmartScreen: o executável não tem assinatura de código | **Mais informações** > **Executar assim mesmo** ([agentes.md](agentes.md#windows-smartscreen-e-antivírus)) |
| `x509: certificate signed by unknown authority` (sem pin) | o servidor não tem certificado público, ou a máquina não confia na CA dele | com certificado da CA da instalação, use o pacote do painel (leva o pin) |

## Depois de instalado

| Mensagem (log do agente) | Causa | O que fazer |
|---|---|---|
| `collected_at … à frente do servidor` (em `queue/rejected/`) | o relógio da máquina está mais de 24h adiantado | acerte o relógio; a próxima coleta entra |
| `o servidor recusou o certificado do agente` (serviço parado, saída 3) | agente revogado, máquina cadastrada de novo (cada cadastro revoga o anterior), ou um proxy no caminho respondendo `401` | se foi revogado, cadastre de novo com o instalador e `-Force`/`--force`; se foi o proxy, corrija-o e reinicie o serviço |
| `envio falhou; snapshot fica na fila` | servidor fora do ar ou fora de alcance | nada a fazer se for passageiro; o agente tenta de novo e a fila guarda até 100 |
| `servidor recusou o snapshot` (em `queue/rejected/`) | recusa definitiva (4xx), com o motivo no arquivo | veja o motivo; atrás de proxy, veja a tabela abaixo |

## Proxy reverso

| Sintoma | Causa | O que fazer |
|---|---|---|
| `502 Bad Gateway` | o proxy não alcança o servidor | `docker compose ps`; com o proxy na mesma máquina, `curl http://127.0.0.1:8080/healthz` tem de dar `204` |
| `403` "… fora de --trusted-proxies" | o proxy conecta de um endereço que o servidor não conhece | ponha o IP do proxy em `INVENIRE_TRUSTED_PROXIES` ([proxy em outra máquina](instalar/proxy-reverso.md#proxy-em-outra-máquina)) |
| `403` "o proxy recebeu esta requisição por http" | o proxy repassa o que chegou em HTTP | redirecione HTTP para HTTPS no proxy (`return 301`) |
| `413` / "corpo grande demais" | limite de corpo do proxy abaixo de 32 MiB | `client_max_body_size 32m;` (Nginx) |
| `401` "assinatura inválida" | o proxy alterou o corpo ou o caminho | desligue o que reescreve o corpo; publique na raiz do domínio |
| `401` "crachá do agente obrigatório" | agente antigo (só mTLS) atrás de proxy que termina TLS | atualize o agente, ou use passthrough |
| `401` "Invenire-Timestamp fora da janela" | relógio da máquina mais de 24h longe do servidor (o agente já tentou corrigir) | acerte o relógio da máquina e do servidor |
| agente: `tls: protocol version not supported` | o proxy não oferece TLS 1.3 | `ssl_protocols TLSv1.2 TLSv1.3;` |
| pacote ou comando com endereço `127.0.0.1:8080` ou `http://` | o proxy não repassa o `Host` | `proxy_set_header Host $host;` |

## Painel

| Sintoma | Causa | O que fazer |
|---|---|---|
| aviso de "conexão não segura" | modo direto: o navegador não conhece a CA da instalação | esperado na rede local (**Avançado** > **Continuar**); para não ter aviso, use [proxy](instalar/proxy-reverso.md) ou [VPS](instalar/vps.md) |
| janela pedindo para escolher um certificado | modo direto: o servidor pede certificado cliente por causa dos agentes, e o navegador tem e-CPF/e-CNPJ | **Cancelar** |
| "Código de configuração errado." | código de outra subida (cada subida sem organização gera outro) | pegue o da linha mais recente: `docker compose logs server \| grep "primeira configuração"` |
| `/setup` leva ao login | a organização já foi criada | entre com o e-mail e a senha do primeiro administrador |
| "E-mail ou senha não conferem." / esqueceu a senha | | `invenire-server user set-password --email <e-mail>` ([operacao.md](operacao.md#linha-de-comando-de-administração)) |
| "Muitas tentativas." | 10 senhas erradas (por e-mail e por endereço) | espere 15 minutos, ou troque a senha pela linha de comando |
| "Seu acesso é só de consulta" | usuário `read` | peça a um administrador |
| "O formulário expirou ou não veio desta página." | sessão nova desde que a página abriu | recarregue a página e tente de novo |
| "O formulário não veio em UTF-8." | formulário enviado por script em outra codificação | envie em UTF-8 |
| "Este servidor não tem os executáveis do agente" | servidor fora do Docker sem `--agent-dir` | aponte `--agent-dir` (ou `INVENIRE_AGENT_DIR`) para a pasta de `scripts/build.sh` |
| a página do token sumiu depois de recarregar | o token aparece uma vez só | crie outro token |
