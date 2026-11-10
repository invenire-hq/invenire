package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/athosbes/invenire/internal/server/export"
)

// ExportKeyHeader traz a chave de exportação quando o pacote inclui a chave
// da CA (?with_ca_key=true). Só existe nessa resposta.
const ExportKeyHeader = "Invenire-Export-Key"

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// exportTenant: GET /v1/export[?with_ca_key=true]
// O pacote do tenant inteiro, o mesmo do invenire-server tenant export. Ele
// é enviado enquanto é montado: um erro no meio corta a resposta, e o pacote
// sem manifest.json é recusado pelo tenant verify.
func (s *Server) exportTenant(w http.ResponseWriter, r *http.Request) {
	actor := actorFrom(r.Context())
	withKey := r.URL.Query().Get("with_ca_key") == "true"
	opts := export.Options{ServerVersion: s.Version, WithCAKey: withKey, Master: s.master}

	// A chave de exportação precisa ir no cabeçalho, antes do corpo: ela é
	// gerada aqui e passada à exportação.
	t, err := s.q.GetTenant(r.Context(), actor.TenantID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	name := unsafeFileChars.ReplaceAllString(t.Name, "_") + "-" + time.Now().UTC().Format("20060102-150405") + ".invenire.tar.gz"
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Cache-Control", "no-store")
	if withKey {
		opts.ExportKey = export.NewExportKey()
		w.Header().Set(ExportKeyHeader, base64.StdEncoding.EncodeToString(opts.ExportKey))
	}
	res, err := export.Write(r.Context(), s.pool, actor.TenantID, w, opts)
	if err != nil {
		s.log.ErrorContext(r.Context(), "exportação interrompida", "tenant", actor.TenantID, "err", err)
		panic(http.ErrAbortHandler) // corta a conexão: o cliente não recebe um pacote que parece completo
	}
	if err := export.Record(r.Context(), s.q, &actor.ID, res.Manifest, withKey, "api"); err != nil {
		s.log.ErrorContext(r.Context(), "exportação sem registro no change_log", "tenant", actor.TenantID, "err", err)
	}
	s.log.InfoContext(r.Context(), "tenant exportado", "actor", actor.Name, "snapshots", res.Manifest.SnapshotBodies, "with_ca_key", withKey)
}
