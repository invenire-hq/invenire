// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/invenire-hq/invenire/internal/server/store"
)

// Tokens de usuários e pipelines. Escopos (actor.scopes): read consulta;
// admin faz tudo, inclusive mapear software à mão.
const (
	APITokenPrefix = "inva_"
	scopeRead      = "read"
	scopeAdmin     = "admin"
)

// NewAPIToken cria um token de API para o ator. ttl 0 = não expira. O valor
// só existe na resposta: o banco guarda o SHA-256.
func NewAPIToken(ctx context.Context, q *store.Queries, actorID uuid.UUID, description string, ttl time.Duration) (string, *time.Time, error) {
	b := make([]byte, 32)
	rand.Read(b)
	token := APITokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(token))
	var expires *time.Time
	if ttl > 0 {
		e := time.Now().Add(ttl)
		expires = &e
	}
	_, err := q.CreateAPIToken(ctx, store.CreateAPITokenParams{ActorID: actorID, TokenHash: h[:], Description: description, ExpiresAt: expires})
	return token, expires, err
}

type actorKey struct{}

// userAuth exige "Authorization: Bearer inva_…" de um ator ativo com o
// escopo pedido (admin vale por todos).
func (s *Server) userAuth(scope string, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !strings.HasPrefix(token, APITokenPrefix) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "token de API obrigatório (Authorization: Bearer "+APITokenPrefix+"…)")
			return
		}
		h := sha256.Sum256([]byte(token))
		actor, err := s.q.GetActorByAPIToken(r.Context(), h[:])
		if errors.Is(err, pgx.ErrNoRows) {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			writeError(w, http.StatusUnauthorized, "token inválido, expirado ou revogado")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if !slices.Contains(actor.Scopes, scope) && !slices.Contains(actor.Scopes, scopeAdmin) {
			writeError(w, http.StatusForbidden, "escopo "+scope+" necessário")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, actor)))
	})
}

func actorFrom(ctx context.Context) store.Actor {
	return ctx.Value(actorKey{}).(store.Actor)
}
