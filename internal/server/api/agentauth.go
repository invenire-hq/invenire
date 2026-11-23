// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/invenire-hq/invenire/internal/server/pki"
	"github.com/invenire-hq/invenire/internal/server/store"
	"github.com/invenire-hq/invenire/internal/wire"
)

// authedAgent é quem agentAuth deixou passar.
type authedAgent struct {
	agent store.Agent
	// cert é o crachá apresentado (no TLS ou no cabeçalho da assinatura).
	cert *x509.Certificate
	// signedAt é o Invenire-Timestamp da requisição assinada; nulo no mTLS.
	signedAt *time.Time
}

type ctxKey struct{}

func agentFrom(ctx context.Context) store.Agent {
	return ctx.Value(ctxKey{}).(*authedAgent).agent
}

func authFrom(ctx context.Context) *authedAgent {
	return ctx.Value(ctxKey{}).(*authedAgent)
}

// agentAuth exige o crachá de um agente ativo, emitido pela CA (não revogada)
// que o banco diz ter assinado aquele crachá. O crachá chega de um de dois
// jeitos, e a conferência dele é a mesma:
//
//   - mTLS: certificado cliente no TLS. Só no modo direto; o TLS já provou
//     que o cliente tem a chave. Tem preferência, porque não depende do
//     relógio da máquina e porque agentes antigos só sabem fazer assim.
//   - Assinatura: crachá em Invenire-Agent-Cert e assinatura com a chave dele
//     sobre método, caminho, timestamp e corpo (wire.SignedMessage). É o que
//     funciona atrás de um proxy que termina o TLS, e o único aceito no modo
//     --behind-proxy.
//
// maxBody é o limite do corpo como enviado. Na assinatura o corpo é lido
// inteiro aqui (o hash dele está na assinatura) e entregue ao handler da
// memória; o limite é conferido antes de ler.
func (s *Server) agentAuth(maxBody int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var a *authedAgent
		var ok bool
		switch {
		case !s.behindProxy && r.TLS != nil && len(r.TLS.PeerCertificates) > 0:
			a, ok = s.authTLS(w, r)
		case r.Header.Get(wire.AgentCertHeader) != "":
			a, ok = s.authSignature(w, r, maxBody)
		default:
			writeError(w, http.StatusUnauthorized, "crachá do agente obrigatório (certificado cliente ou assinatura)")
			return
		}
		if ok {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, a)))
		}
	})
}

func (s *Server) authTLS(w http.ResponseWriter, r *http.Request) (*authedAgent, bool) {
	agent, ok := s.checkBadge(w, r, r.TLS.PeerCertificates)
	if !ok {
		return nil, false
	}
	return &authedAgent{agent: agent, cert: r.TLS.PeerCertificates[0]}, true
}

func (s *Server) authSignature(w http.ResponseWriter, r *http.Request, maxBody int64) (*authedAgent, bool) {
	der, err := base64.StdEncoding.DecodeString(r.Header.Get(wire.AgentCertHeader))
	if err != nil {
		writeError(w, http.StatusUnauthorized, wire.AgentCertHeader+" inválido")
		return nil, false
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		writeError(w, http.StatusUnauthorized, wire.AgentCertHeader+" inválido")
		return nil, false
	}
	ts := r.Header.Get(wire.TimestampHeader)
	signedAt, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		writeError(w, http.StatusUnauthorized, wire.TimestampHeader+" inválido (esperava RFC 3339)")
		return nil, false
	}
	if d := time.Since(signedAt); d > wire.SignatureWindow || d < -wire.SignatureWindow {
		s.log.WarnContext(r.Context(), "assinatura fora da janela", "remote", r.RemoteAddr, "timestamp", ts)
		writeError(w, http.StatusUnauthorized, wire.TimestampHeader+" fora da janela: confira o relógio da máquina")
		return nil, false
	}
	sig, err := base64.StdEncoding.DecodeString(r.Header.Get(wire.SignatureHeader))
	if err != nil || len(sig) == 0 {
		writeError(w, http.StatusUnauthorized, wire.SignatureHeader+" ausente ou inválido")
		return nil, false
	}

	// Crachá conhecido e válido antes de ler o corpo: quem não tem crachá
	// não faz o servidor ler até maxBody.
	agent, ok := s.checkBadge(w, r, []*x509.Certificate{cert})
	if !ok {
		return nil, false
	}

	if r.ContentLength > maxBody {
		writeErrorCode(w, http.StatusRequestEntityTooLarge, wire.ErrorCodeTooLarge, "corpo grande demais")
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		writeErrorCode(w, http.StatusRequestEntityTooLarge, wire.ErrorCodeTooLarge, "corpo grande demais")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "corpo ilegível: "+err.Error())
		return nil, false
	}
	if err := verifySignature(cert, wire.SignedMessage(r.Method, r.URL.Path, ts, body), sig); err != nil {
		s.log.WarnContext(r.Context(), "assinatura recusada", "agent_id", agent.ID, "remote", r.RemoteAddr, "err", err)
		writeError(w, http.StatusUnauthorized, "assinatura inválida")
		return nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return &authedAgent{agent: agent, cert: cert, signedAt: &signedAt}, true
}

// checkBadge confere o crachá (chain[0]) contra o banco: agente não revogado,
// CA não revogada, e a cadeia emitida por essa CA, dentro da validade.
func (s *Server) checkBadge(w http.ResponseWriter, r *http.Request, chain []*x509.Certificate) (store.Agent, bool) {
	row, err := s.q.GetAgentForAuth(r.Context(), pki.Fingerprint(chain[0].Raw))
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorCode(w, http.StatusUnauthorized, wire.ErrorCodeAgentRevoked, "agente desconhecido ou revogado")
		return store.Agent{}, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return store.Agent{}, false
	}
	if err := pki.VerifyAgentCert(chain, row.CaCert); err != nil {
		s.log.WarnContext(r.Context(), "crachá recusado", "agent_id", row.Agent.ID, "remote", r.RemoteAddr, "err", err)
		writeErrorCode(w, http.StatusUnauthorized, wire.ErrorCodeBadgeInvalid, "certificado cliente inválido ou vencido")
		return store.Agent{}, false
	}
	return row.Agent, true
}

// verifySignature confere sig sobre msg com a chave pública do crachá, no
// mesmo esquema que transport.SignRequest usa para cada tipo de chave.
func verifySignature(cert *x509.Certificate, msg, sig []byte) error {
	var algo x509.SignatureAlgorithm
	switch cert.PublicKey.(type) {
	case *ecdsa.PublicKey:
		algo = x509.ECDSAWithSHA256
	case *rsa.PublicKey:
		algo = x509.SHA256WithRSA
	case ed25519.PublicKey:
		algo = x509.PureEd25519
	default:
		return errors.New("tipo de chave do crachá não suportado")
	}
	return cert.CheckSignature(algo, msg, sig)
}
