package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/athosbes/invenire/internal/server/pki"
	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/internal/wire"
	"github.com/athosbes/invenire/pkg/snapshot"
)

// NewEnrollmentToken cria um token de cadastro de uso único. O valor só
// existe na resposta: o banco guarda o SHA-256. unitID, se não for nulo, é a
// unidade (do mesmo tenant) onde a máquina cadastrada entra.
func NewEnrollmentToken(ctx context.Context, q *store.Queries, tenantID uuid.UUID, unitID *uuid.UUID, description string, ttl time.Duration) (token string, expires time.Time, err error) {
	t, err := CreateEnrollmentToken(ctx, q, TokenSpec{Tenant: tenantID, Unit: unitID, Description: description, TTL: ttl, Uses: 1})
	return t.Token, t.ExpiresAt, err
}

// TokenSpec descreve um token de cadastro a criar.
type TokenSpec struct {
	Tenant      uuid.UUID
	Unit        *uuid.UUID
	Description string
	TTL         time.Duration
	// Uses é quantas máquinas o token cadastra (1 a 10000). Mais de um
	// serve a um lote, como o piloto; cada uso fica no change_log.
	Uses int
}

// NewToken é um token de cadastro recém-criado.
type NewToken struct {
	ID        uuid.UUID
	Token     string // só existe aqui: o banco guarda o SHA-256
	ExpiresAt time.Time
}

// CreateEnrollmentToken cria um token de cadastro com o número de usos pedido.
func CreateEnrollmentToken(ctx context.Context, q *store.Queries, spec TokenSpec) (NewToken, error) {
	if spec.Uses < 1 || spec.Uses > 10000 {
		return NewToken{}, errors.New("usos do token: de 1 a 10000")
	}
	b := make([]byte, 32)
	rand.Read(b)
	token := wire.TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(token))
	row, err := q.CreateEnrollmentToken(ctx, store.CreateEnrollmentTokenParams{
		TenantID:    spec.Tenant,
		TokenHash:   h[:],
		Description: spec.Description,
		ExpiresAt:   time.Now().Add(spec.TTL),
		UnitID:      spec.Unit,
		MaxUses:     int32(spec.Uses),
	})
	return NewToken{ID: row.ID, Token: token, ExpiresAt: row.ExpiresAt}, err
}

const maxEnrollBody = 64 << 10

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var req wire.EnrollRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEnrollBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "corpo inválido: "+err.Error())
		return
	}
	if !strings.HasPrefix(req.Token, wire.TokenPrefix) || req.CSR == "" || req.AgentVersion == "" {
		writeError(w, http.StatusBadRequest, "token, csr e agent_version são obrigatórios")
		return
	}
	fingerprint := snapshot.Fingerprint(req.Host)
	if fingerprint == "" {
		writeError(w, http.StatusUnprocessableEntity, "host sem smbios_uuid nem machine_id: ativo não identificável")
		return
	}

	var resp wire.EnrollResponse
	err := s.inTx(r.Context(), func(q *store.Queries) error {
		h := sha256.Sum256([]byte(req.Token))
		tok, err := q.ConsumeEnrollmentToken(r.Context(), h[:])
		if errors.Is(err, pgx.ErrNoRows) {
			// Não diz se o token não existe, expirou ou já foi usado.
			return errorf(http.StatusUnauthorized, "token de cadastro inválido")
		}
		if err != nil {
			return err
		}

		asset, err := q.UpsertAsset(r.Context(), store.UpsertAssetParams{
			TenantID:     tok.TenantID,
			Fingerprint:  fingerprint,
			Hostname:     nonEmpty(req.Host.Hostname),
			Serial:       nonEmpty(req.Host.Serial),
			Manufacturer: nonEmpty(req.Host.Manufacturer),
			Model:        nonEmpty(req.Host.Model),
			OsFamily:     nonEmpty(req.OS.Family),
			OsName:       nonEmpty(req.OS.Name),
			OsVersion:    nonEmpty(req.OS.Version),
		})
		if err != nil {
			return err
		}

		ca, caID, err := s.signingCA(r.Context(), q, tok.TenantID)
		if err != nil {
			return err
		}
		agentID := uuid.New()
		certPEM, certFP, err := ca.SignAgentCSR([]byte(req.CSR), agentID.String())
		if err != nil {
			return errorf(http.StatusBadRequest, "%s", err)
		}
		// Um ativo, um agente: o cadastro novo revoga os crachás anteriores. O
		// fingerprint é declarado por quem se cadastra, então é isto que faz um
		// cadastro no lugar de outra máquina aparecer (o agente dela para com
		// 401) em vez de conviver com ela em silêncio.
		replaced, err := q.RevokeAssetAgents(r.Context(), asset.ID)
		if err != nil {
			return err
		}
		if _, err := q.CreateAgent(r.Context(), store.CreateAgentParams{
			ID:              agentID,
			AssetID:         asset.ID,
			CertFingerprint: certFP,
			AgentVersion:    req.AgentVersion,
			TenantCaID:      &caID,
		}); err != nil {
			return err
		}
		// A unidade do token só vale para ativo sem unidade: recadastrar não
		// tira a máquina do lugar que alguém escolheu.
		var movedTo *uuid.UUID
		if tok.UnitID != nil {
			if _, err := q.SetAssetUnitIfNone(r.Context(), store.SetAssetUnitIfNoneParams{ID: asset.ID, UnitID: tok.UnitID}); err == nil {
				movedTo = tok.UnitID
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if err := q.MarkEnrollmentTokenUsed(r.Context(), store.MarkEnrollmentTokenUsedParams{ID: tok.ID, UsedBy: &agentID}); err != nil {
			return err
		}

		actor, err := q.EnsureSystemActor(r.Context(), store.EnsureSystemActorParams{TenantID: tok.TenantID, Name: "enroll"})
		if err != nil {
			return err
		}
		if asset.Inserted {
			if err := appendChange(r.Context(), q, tok.TenantID, "asset_new", &asset.ID, &actor.ID,
				map[string]any{"fingerprint": fingerprint, "hostname": req.Host.Hostname}); err != nil {
				return err
			}
		}
		if err := appendChange(r.Context(), q, tok.TenantID, "agent_enrolled", &asset.ID, &actor.ID,
			map[string]any{"agent_id": agentID, "agent_version": req.AgentVersion, "token_id": tok.ID, "revoked_agents": replaced}); err != nil {
			return err
		}
		if movedTo != nil {
			if _, err := appendAssetMoved(r.Context(), q, tok.TenantID, asset.ID, &actor.ID, nil, movedTo); err != nil {
				return err
			}
		}

		resp = wire.EnrollResponse{AgentID: agentID, AssetID: asset.ID, Certificate: string(certPEM)}
		if s.serverCA != nil {
			resp.CA = string(s.serverCA.PEM())
		}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.log.InfoContext(r.Context(), "agente cadastrado", "agent_id", resp.AgentID, "asset_id", resp.AssetID, "hostname", req.Host.Hostname)
	writeJSON(w, http.StatusCreated, resp)
}

const maxRenewBody = 16 << 10

// renewAgent: POST /v1/agent/renew. O agente, autenticado com o crachá que
// tem, manda um CSR e recebe um crachá novo da CA que assina hoje para o
// tenant. O crachá com que ele se apresentou continua valendo até vencer,
// para uma resposta perdida no caminho não o deixar de fora.
func (s *Server) renewAgent(w http.ResponseWriter, r *http.Request) {
	agent := agentFrom(r.Context())
	var req wire.RenewRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRenewBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "corpo inválido: "+err.Error())
		return
	}
	auth := authFrom(r.Context())
	presented := pki.Fingerprint(auth.cert.Raw)

	var resp wire.RenewResponse
	err := s.inTx(r.Context(), func(q *store.Queries) error {
		asset, err := q.GetAsset(r.Context(), agent.AssetID)
		if err != nil {
			return err
		}
		ca, caID, err := s.signingCA(r.Context(), q, asset.TenantID)
		if err != nil {
			return err
		}
		certPEM, certFP, err := ca.SignAgentCSR([]byte(req.CSR), agent.ID.String())
		if err != nil {
			return errorf(http.StatusBadRequest, "%s", err)
		}
		if _, err := q.RenewAgentCert(r.Context(), store.RenewAgentCertParams{
			ID: agent.ID, Presented: presented, CertFingerprint: certFP, TenantCaID: &caID, SignedAt: auth.signedAt,
		}); errors.Is(err, pgx.ErrNoRows) {
			// Revogado, ou outra renovação trocou o crachá, entre o agentAuth e
			// aqui; ou, na assinatura, timestamp não maior que o da última
			// renovação assinada (requisição repetida, migração 00011).
			return errorf(http.StatusUnauthorized, "agente desconhecido ou revogado, ou renovação repetida")
		} else if err != nil {
			return err
		}
		actor, err := q.EnsureSystemActor(r.Context(), store.EnsureSystemActorParams{TenantID: asset.TenantID, Name: "pki"})
		if err != nil {
			return err
		}
		resp.Certificate = string(certPEM)
		return appendChange(r.Context(), q, asset.TenantID, "agent_cert_renewed", &asset.ID, &actor.ID,
			map[string]any{"agent_id": agent.ID, "tenant_ca_id": caID, "fingerprint": certFP, "previous_fingerprint": presented})
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.log.InfoContext(r.Context(), "crachá renovado", "agent_id", agent.ID)
	writeJSON(w, http.StatusOK, resp)
}

func appendChange(ctx context.Context, q *store.Queries, tenantID uuid.UUID, kind string, assetID, actorID *uuid.UUID, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = q.AppendChange(ctx, store.AppendChangeParams{TenantID: tenantID, Kind: kind, AssetID: assetID, ActorID: actorID, Data: b})
	return err
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
