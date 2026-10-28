// Package api tem os handlers HTTP do invenire-server.
//
// Toda a API roda sobre TLS com a CA interna. POST /v1/enroll é a única rota
// que aceita cliente sem certificado: é por ela que o agente o obtém. As
// demais rotas de agente exigem o certificado cliente e um agente não revogado.
package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/athosbes/invenire/internal/server/pki"
	"github.com/athosbes/invenire/internal/server/store"
)

// Server guarda as dependências dos handlers.
type Server struct {
	pool   *pgxpool.Pool
	q      *store.Queries
	ca     *pki.CA
	schema *jsonschema.Schema
	log    *slog.Logger
}

func New(pool *pgxpool.Pool, ca *pki.CA, log *slog.Logger) (*Server, error) {
	schema, err := compileSnapshotSchema()
	if err != nil {
		return nil, err
	}
	return &Server{pool: pool, q: store.New(pool), ca: ca, schema: schema, log: log}, nil
}

// Handler devolve as rotas da API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.pool.Ping(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "banco indisponível")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/enroll", s.enroll)
	mux.Handle("POST /v1/snapshots", s.agentAuth(http.HandlerFunc(s.postSnapshot)))
	return mux
}

// TLSConfig monta o TLS do servidor: o certificado cliente é pedido e, se
// vier, validado contra a CA interna; quem exige que ele exista é a rota.
func (s *Server) TLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    s.ca.Pool(),
	}
}

type ctxKey struct{}

// agentAuth exige um certificado cliente emitido pela CA e de um agente ativo.
func (s *Server) agentAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			writeError(w, http.StatusUnauthorized, "certificado cliente obrigatório")
			return
		}
		leaf := r.TLS.VerifiedChains[0][0]
		agent, err := s.q.GetActiveAgentByCert(r.Context(), pki.Fingerprint(leaf.Raw))
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "agente desconhecido ou revogado")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, agent)))
	})
}

func agentFrom(ctx context.Context) store.Agent {
	return ctx.Value(ctxKey{}).(store.Agent)
}

// inTx roda fn numa transação e faz commit se ela não falhar.
func (s *Server) inTx(ctx context.Context, fn func(q *store.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// httpError é um erro com status e mensagem para o cliente.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func errorf(status int, format string, args ...any) error {
	return &httpError{status, fmt.Sprintf(format, args...)}
}

// fail responde com o status de um httpError, ou 500 para qualquer outro erro.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var he *httpError
	if errors.As(err, &he) {
		writeError(w, he.status, he.msg)
		return
	}
	s.internalError(w, r, err)
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.ErrorContext(r.Context(), "erro interno", "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "erro interno")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
