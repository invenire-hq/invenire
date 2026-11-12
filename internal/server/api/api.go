// Package api tem os handlers HTTP do invenire-server.
//
// A API roda sobre TLS, direto ou num proxy reverso que termina o TLS
// (--behind-proxy). POST /v1/enroll é a única rota de agente que aceita
// cliente sem crachá: é por ela que o agente obtém o crachá, assinado pela CA
// do tenant. As demais rotas de agente exigem o crachá, de um agente não
// revogado, conferido contra a CA que o emitiu (agentauth.go).
package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

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
	// behindProxy: o TLS termina num proxy, e o agente só se autentica
	// por assinatura.
	behindProxy bool

	// Wake, se definido, é chamado quando há trabalho para a ingestão: um
	// snapshot novo (não repetido) ou um mapeamento manual. O serve liga aqui
	// o Wake do worker.
	Wake func()

	// Version é a versão do servidor, gravada nos pacotes de exportação.
	Version string

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
	// BehindProxy diz que o servidor fala HTTP com um proxy reverso que
	// termina o TLS. O certificado cliente não chega, então o agente só é
	// aceito pela assinatura da requisição. Exige ServerCA nula: o agente
	// confere o certificado (público) do proxy pelas CAs do sistema.
	BehindProxy bool
}

func New(pool *pgxpool.Pool, cfg Config, log *slog.Logger) (*Server, error) {
	if cfg.MasterKey == nil {
		return nil, errors.New("api: chave-mestra obrigatória")
	}
	if cfg.BehindProxy && cfg.ServerCA != nil {
		return nil, errors.New("api: atrás de proxy o certificado é do proxy, não da CA da instalação")
	}
	schema, err := compileSnapshotSchema()
	if err != nil {
		return nil, err
	}
	return &Server{pool: pool, q: store.New(pool), serverCA: cfg.ServerCA, master: cfg.MasterKey, schema: schema, log: log, behindProxy: cfg.BehindProxy}, nil
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
	mux.Handle("POST "+wire.PathSnapshots, s.agentAuth(maxSnapshotWire, http.HandlerFunc(s.postSnapshot)))
	mux.Handle("GET "+wire.PathAgent, s.agentAuth(0, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := agentFrom(r.Context())
		writeJSON(w, http.StatusOK, wire.AgentInfo{AgentID: a.ID, AssetID: a.AssetID})
	})))
	mux.Handle("POST "+wire.PathAgentRenew, s.agentAuth(maxRenewBody, http.HandlerFunc(s.renewAgent)))
	mux.Handle("GET /v1/software/unmapped", s.userAuth(scopeRead, s.listUnmapped))
	mux.Handle("PUT /v1/software/raw/{id}", s.userAuth(scopeAdmin, s.mapRaw))
	mux.Handle("DELETE /v1/software/raw/{id}", s.userAuth(scopeAdmin, s.unmapRaw))
	mux.Handle("GET /v1/units", s.userAuth(scopeRead, s.listUnits))
	mux.Handle("POST /v1/units", s.userAuth(scopeAdmin, s.createUnit))
	mux.Handle("PATCH /v1/units/{id}", s.userAuth(scopeAdmin, s.updateUnit))
	mux.Handle("DELETE /v1/units/{id}", s.userAuth(scopeAdmin, s.deleteUnit))
	mux.Handle("PUT /v1/assets/{id}/unit", s.userAuth(scopeAdmin, s.setAssetUnit))
	mux.Handle("GET /v1/assets/{id}/labels", s.userAuth(scopeRead, s.listLabels))
	mux.Handle("PUT /v1/assets/{id}/labels/{key}", s.userAuth(scopeAdmin, s.setLabel))
	mux.Handle("DELETE /v1/assets/{id}/labels/{key}", s.userAuth(scopeAdmin, s.deleteLabel))
	mux.Handle("GET /v1/assets", s.userAuth(scopeRead, s.listAssets))
	mux.Handle("GET /v1/assets/{id}", s.userAuth(scopeRead, s.getAsset))
	mux.Handle("GET /v1/assets/{id}/software", s.userAuth(scopeRead, s.assetSoftware))
	mux.Handle("GET /v1/assets/{id}/history", s.userAuth(scopeRead, s.assetHistory))
	mux.Handle("GET /v1/software", s.userAuth(scopeRead, s.listSoftware))
	mux.Handle("GET /v1/software/{id}/installations", s.userAuth(scopeRead, s.softwareInstallations))
	mux.Handle("GET /v1/changes", s.userAuth(scopeRead, s.listChanges))
	mux.Handle("GET /v1/export", s.userAuth(scopeAdmin, s.exportTenant))
	return mux
}

// TLSConfig monta o TLS do servidor no modo direto. O crachá é pedido mas não
// conferido no handshake: cada tenant tem a sua CA, e com milhares deles a
// lista de CAs aceitas não cabe no handshake. Quem confere é agentAuth, contra
// a CA que emitiu aquele crachá; o TLS já garante que o cliente tem a chave
// dele.
func (s *Server) TLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequestClientCert,
	}
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
