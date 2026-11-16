# Documentação do Invenire

Em português (pt-BR). Outros idiomas, quando houver, ficam em pastas próprias (`docs/en/`, por
exemplo), com os mesmos nomes de arquivo.

## Instalar o servidor

- [Qual cenário é o seu](instalar/escolha.md)
- [Rede local](instalar/rede-local.md): sem domínio, porta 8443, agentes com pin
- [VPS](instalar/vps.md): domínio, Caddy e Let's Encrypt automático
- [Proxy reverso](instalar/proxy-reverso.md): Nginx (receita completa) e a matriz de outros proxies

## Usar e manter

- [Painel web](painel.md): primeira configuração e telas
- [Agentes](agentes.md): instalar pelos três caminhos, atualizar, trocar de servidor, desinstalar, logs
- [Operação](operacao.md): backup, restauração, atualização, monitoramento, linha de comando
- [Quando dá errado](problemas.md): mensagens, estados das máquinas, proxy e painel
- [Roteiro do piloto](piloto.md): ordem, critérios de sucesso e o que anotar

## Referência

- [Segurança](seguranca.md): quem confia em quem, o que o proxy enxerga, painel e pacote
- [API](api.md): rotas `/v1` e consultas
- [Como funciona por dentro](funcionamento.md): unidades, ingestão, normalização
- [Exportação da organização](exportacao.md): formato do pacote e importação futura
- [Desenvolvimento](desenvolvimento.md): estrutura, build, testes, sqlc
- [Plano: instalação simples e documentação](plano-instalacao-simples.md) (histórico)
