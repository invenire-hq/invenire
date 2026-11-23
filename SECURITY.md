# Segurança

## Como relatar uma falha

**Não abra issue público** para falha de segurança: ele fica visível para todos antes de haver
correção.

Use o relatório privado do GitHub: na página do repositório, aba **Security** > **Report a
vulnerability** (ou direto em
<https://github.com/invenire-hq/invenire/security/advisories/new>). Só os mantenedores veem o
relato, e a conversa sobre a correção acontece ali.

Ajuda muito incluir:

- versão do servidor e do agente (`invenire-server version`, `invenire-agent version`);
- cenário de instalação (rede local, VPS com Caddy, atrás de proxy; qual proxy);
- o que a falha permite fazer, e por quem (alguém na rede, um agente cadastrado, um usuário
  `read` do painel, alguém sem conta);
- passos para reproduzir ou prova de conceito.

Não inclua dados reais de inventário, tokens ou chaves que estejam em uso: troque por exemplos.

## O que esperar

- Confirmação de recebimento em até 7 dias.
- Avaliação e plano de correção combinados no próprio relato.
- Correção publicada numa versão nova, com aviso de segurança (GitHub Security Advisory) e
  linha no [CHANGELOG.md](CHANGELOG.md). Quem relatou recebe o crédito, se quiser.

O Invenire é mantido por pouca gente: os prazos acima são de boa-fé, não um contrato de
atendimento.

## Versões com correção

Só a versão mais recente recebe correções de segurança. Atualize o servidor
([docs/operacao.md](docs/operacao.md#atualizar-o-servidor)) e os agentes
([docs/agentes.md](docs/agentes.md#atualizar)).

## Como o Invenire se protege

O modelo de confiança (CA da instalação e CA de cada organização, crachá do agente, assinatura
de requisição atrás de proxy, código da primeira configuração, sessões do painel, dados
guardados) está em [docs/seguranca.md](docs/seguranca.md). Comportamento descrito ali como
esperado não é falha; se achar que a decisão está errada, um issue comum serve.
