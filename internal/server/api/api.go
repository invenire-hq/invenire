// Package api tem os handlers HTTP do invenire-server.
//
// Toda a API roda sobre TLS. POST /v1/enroll é a única rota de agente que
// aceita cliente sem certificado: é por ela que o agente obtém o crachá,
// assinado pela CA do tenant. As demais rotas de agente exigem o crachá, de um
// agente não revogado, conferido contra a CA que o emitiu.
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

	"github.com/athosbes/invenire/internal/server/normalize"
	"github.com/athosbes/invenire/internal/server/pki"
	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/internal/wire"
)

// Server guarda as dependências dos handlers.
type Server struct {
	pool     *pgxpool.Pool
	q        *store.Queries
	serverCA *pki.CA // nil quando o servidor usa certificado público
	master   *pki.MasterKey
	schema   *jsonschema.Schema
	log      *slog.Logger

	// Wake, se definido, é chamado quando há trabalho para a ingestão: um
	// snapshot novo (não repetido) ou um mapeamento manual. O serve liga aqui
	// o Wake do worker.
	Wake func()

	// Suggest, se definido, dá o palpite das regras para cada nome da lista
	// de não identificados.
	Suggest func(source, rawName, rawVendor string) (normalize.Match, bool)
}

// Config são as chaves do servidor.
type Config struct {
	// ServerCA assina o certificado TLS do servidor e vai na resposta do
	// cadastro, para o agente conferir o servidor dali em diante. Nula quando
	// o servidor usa um certificado público: o agente confia no sistema.
	ServerCA *pki.CA
	// MasterKey abre as chaves das CAs dos tenants.
	MasterKey *pki.MasterKey
}

func New(pool *pgxpool.Pool, cfg Config, log *slog.Logger) (*Server, error) {
	if cfg.MasterKey == nil {
		return nil, errors.New("api: chave-mestra obrigatória")
	}
	schema, err := compileSnapshotSchema()
	if err != nil {
		return nil, err
	}
	return &Server{pool: pool, q: store.New(pool), serverCA: cfg.ServerCA, master: cfg.MasterKey, schema: schema, log: log}, nil
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
	mux.HandleFunc("POST "+wire.PathEnroll, s.enroll)
	mux.Handle("POST "+wire.PathSnapshots, s.agentAuth(http.HandlerFunc(s.postSnapshot)))
	mux.Handle("GET "+wire.PathAgent, s.agentAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := agentFrom(r.Context())
		writeJSON(w, http.StatusOK, wire.AgentInfo{AgentID: a.ID, AssetID: a.AssetID})
	})))
	mux.Handle("GET /v1/software/unmapped", s.userAuth(scopeRead, s.listUnmapped))
	mux.Handle("PUT /v1/software/raw/{id}", s.userAuth(scopeAdmin, s.mapRaw))
	return mux
}

// TLSConfig monta o TLS do servidor. O crachá é pedido mas não conferido no
// handshake: cada tenant tem a sua CA, e com milhares deles a lista de CAs
// aceitas não cabe no handshake. Quem confere é agentAuth, contra a CA que
// emitiu aquele crachá; o TLS já garante que o cliente tem a chave dele.
func (s *Server) TLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequestClientCert,
	}
}

type ctxKey struct{}

// agentAuth exige o crachá de um agente ativo, emitido pela CA (não revogada)
// que o banco diz ter assinado aquele crachá.
func (s *Server) agentAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			writeError(w, http.StatusUnauthorized, "certificado cliente obrigatório")
			return
		}
		row, err := s.q.GetAgentForAuth(r.Context(), pki.Fingerprint(r.TLS.PeerCertificates[0].Raw))
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "agente desconhecido ou revogado")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if err := pki.VerifyAgentCert(r.TLS.PeerCertificates, row.CaCert); err != nil {
			s.log.WarnContext(r.Context(), "crachá recusado", "agent_id", row.Agent.ID, "err", err)
			writeError(w, http.StatusUnauthorized, "certificado cliente inválido ou vencido")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, row.Agent)))
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
	writeJSON(w, status, wire.Error{Error: msg})
}
