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

	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/pkg/snapshot"
)

const tokenPrefix = "inv_"

// NewEnrollmentToken cria um token de cadastro de uso único. O valor só
// existe na resposta: o banco guarda o SHA-256.
func NewEnrollmentToken(ctx context.Context, q *store.Queries, tenantID uuid.UUID, description string, ttl time.Duration) (token string, expires time.Time, err error) {
	b := make([]byte, 32)
	rand.Read(b)
	token = tokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(token))
	row, err := q.CreateEnrollmentToken(ctx, store.CreateEnrollmentTokenParams{
		TenantID:    tenantID,
		TokenHash:   h[:],
		Description: description,
		ExpiresAt:   time.Now().Add(ttl),
	})
	return token, row.ExpiresAt, err
}

// EnrollRequest é o corpo de POST /v1/enroll.
type EnrollRequest struct {
	Token        string        `json:"token"`
	CSR          string        `json:"csr"` // PEM, CERTIFICATE REQUEST
	AgentVersion string        `json:"agent_version"`
	Host         snapshot.Host `json:"host"`
	OS           snapshot.OS   `json:"os"`
}

// EnrollResponse devolve o certificado do agente e a CA, que o agente passa
// a usar para validar o servidor.
type EnrollResponse struct {
	AgentID     uuid.UUID `json:"agent_id"`
	AssetID     uuid.UUID `json:"asset_id"`
	Certificate string    `json:"certificate"`
	CA          string    `json:"ca"`
}

const maxEnrollBody = 64 << 10

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var req EnrollRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEnrollBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "corpo inválido: "+err.Error())
		return
	}
	if !strings.HasPrefix(req.Token, tokenPrefix) || req.CSR == "" || req.AgentVersion == "" {
		writeError(w, http.StatusBadRequest, "token, csr e agent_version são obrigatórios")
		return
	}
	fingerprint := snapshot.Fingerprint(req.Host)
	if fingerprint == "" {
		writeError(w, http.StatusUnprocessableEntity, "host sem smbios_uuid nem machine_id: ativo não identificável")
		return
	}

	var resp EnrollResponse
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

		agentID := uuid.New()
		certPEM, certFP, err := s.ca.SignAgentCSR([]byte(req.CSR), agentID.String())
		if err != nil {
			return errorf(http.StatusBadRequest, "%s", err)
		}
		if _, err := q.CreateAgent(r.Context(), store.CreateAgentParams{
			ID:              agentID,
			AssetID:         asset.ID,
			CertFingerprint: certFP,
			AgentVersion:    req.AgentVersion,
		}); err != nil {
			return err
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
			map[string]any{"agent_id": agentID, "agent_version": req.AgentVersion, "token_id": tok.ID}); err != nil {
			return err
		}

		resp = EnrollResponse{AgentID: agentID, AssetID: asset.ID, Certificate: string(certPEM), CA: string(s.ca.PEM())}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.log.InfoContext(r.Context(), "agente cadastrado", "agent_id", resp.AgentID, "asset_id", resp.AssetID, "hostname", req.Host.Hostname)
	writeJSON(w, http.StatusCreated, resp)
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
