// SPDX-License-Identifier: AGPL-3.0-only

package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Assinatura de requisição: o agente prova que tem a chave do crachá sem
// depender do TLS chegar até o servidor (proxy que termina TLS). Toda
// requisição autenticada leva os três cabeçalhos abaixo; o servidor confere a
// assinatura com a chave pública do crachá e, daí em diante, trata o crachá
// como trataria o certificado cliente do mTLS.
const (
	// AgentCertHeader leva o crachá do agente: DER em base64 padrão.
	AgentCertHeader = "Invenire-Agent-Cert"
	// TimestampHeader leva a hora da assinatura, em RFC 3339.
	TimestampHeader = "Invenire-Timestamp"
	// SignatureHeader leva a assinatura, em base64 padrão, de SignedMessage.
	SignatureHeader = "Invenire-Signature"
)

// SignatureWindow é quanto Invenire-Timestamp pode se afastar do relógio do
// servidor, para trás ou para frente. É largo de propósito: relógio errado é
// comum nas máquinas do parque, e a janela só serve para limitar por quanto
// tempo uma requisição capturada poderia ser repetida. Mesma folga do
// collected_at dos snapshots.
const SignatureWindow = 24 * time.Hour

// signatureVersion separa esta assinatura de qualquer outra feita com a mesma
// chave (o CSR da renovação, por exemplo) e de versões futuras do formato.
const signatureVersion = "INVENIRE-SIG-1"

// SignedMessage é o que o agente assina: versão, método, caminho da API (sem
// prefixo de proxy nem query), timestamp exatamente como vai no cabeçalho e
// SHA-256 (hex) do corpo como enviado, isto é, já em gzip quando for o caso.
// O Host fica de fora: proxies podem reescrevê-lo.
func SignedMessage(method, path, timestamp string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(signatureVersion + "\n" + method + "\n" + path + "\n" + timestamp + "\n" + hex.EncodeToString(sum[:]))
}
