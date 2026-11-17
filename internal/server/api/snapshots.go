package api

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/internal/wire"
	"github.com/athosbes/invenire/pkg/snapshot"
)

const (
	maxSnapshotWire = 32 << 20  // corpo como chega, em gzip
	maxSnapshotBody = 128 << 20 // depois de descomprimido

	// maxClockAhead é quanto o collected_at pode estar à frente do relógio do
	// servidor. A ingestão ordena por collected_at: um snapshot datado do
	// futuro faria todos os seguintes do ativo chegarem "fora de ordem", e o
	// estado dele nunca mais mudaria. Um dia cobre fuso mal configurado.
	maxClockAhead = 24 * time.Hour
)

func compileSnapshotSchema() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(snapshot.SchemaV1))
	if err != nil {
		return nil, err
	}
	const url = "urn:invenire:schema:snapshot:v1"
	c := jsonschema.NewCompiler()
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	return c.Compile(url)
}

func (s *Server) postSnapshot(w http.ResponseWriter, r *http.Request) {
	agent := agentFrom(r.Context())

	body, err := readBody(w, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sum := snapshot.Sum(body)
	if want := strings.ToLower(r.Header.Get(wire.SHA256Header)); want != sum {
		writeError(w, http.StatusBadRequest, "SHA-256 do corpo não confere com "+wire.SHA256Header)
		return
	}

	snap, err := s.validate(body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// 422: o agente tira o snapshot da fila (rejected/) em vez de reenviar.
	if ahead := time.Until(snap.CollectedAt); ahead > maxClockAhead {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf(
			"collected_at (%s) está %s à frente do servidor: confira o relógio da máquina",
			snap.CollectedAt.UTC().Format(time.RFC3339), ahead.Round(time.Minute)))
		return
	}
	if snap.AgentID != "" && snap.AgentID != agent.ID.String() {
		writeError(w, http.StatusUnprocessableEntity, "agent_id do snapshot não é o do certificado")
		return
	}
	var prev []byte
	if snap.PrevSHA256 != "" {
		prev, _ = hex.DecodeString(snap.PrevSHA256) // o schema já garantiu o formato
	}
	rawSum, _ := hex.DecodeString(sum)

	var resp wire.SnapshotResponse
	err = s.inTx(r.Context(), func(q *store.Queries) error {
		asset, err := q.GetAsset(r.Context(), agent.AssetID)
		if err != nil {
			return err
		}
		// O certificado é do agente de um ativo; um snapshot de outra máquina
		// com ele é cópia de credencial ou clonagem de disco.
		if fp := snapshot.Fingerprint(snap.Host); fp != asset.Fingerprint {
			return errorf(http.StatusForbidden, "snapshot de outro ativo (%s) com o certificado de %s", fp, asset.Fingerprint)
		}

		last, err := q.GetLastSnapshotHash(r.Context(), asset.ID)
		hasLast := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		row, err := q.InsertSnapshot(r.Context(), store.InsertSnapshotParams{
			AssetID:       asset.ID,
			AgentID:       agent.ID,
			SchemaVersion: snap.SchemaVersion,
			CollectedAt:   snap.CollectedAt,
			Sha256:        rawSum,
			PrevSha256:    prev,
			Payload:       body,
			Body:          body,
		})
		if err != nil {
			return err
		}
		resp = wire.SnapshotResponse{SnapshotID: row.ID, SHA256: sum, Duplicate: !row.Inserted}

		// Divergência no encadeamento vira alerta, não rejeição: o histórico
		// pode ter buracos legítimos (fila local perdida, reinstalação).
		if row.Inserted && hasLast && !bytes.Equal(prev, last) {
			if err := appendChange(r.Context(), q, asset.TenantID, "snapshot_chain_broken", &asset.ID, nil, map[string]any{
				"snapshot_id": row.ID,
				"agent_id":    agent.ID,
				"expected":    hex.EncodeToString(last),
				"prev_sha256": snap.PrevSHA256,
			}); err != nil {
				return err
			}
		}
		return q.TouchAgent(r.Context(), store.TouchAgentParams{ID: agent.ID, AgentVersion: snap.AgentVersion})
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	status := http.StatusAccepted
	if resp.Duplicate {
		status = http.StatusOK
	} else if s.Wake != nil {
		s.Wake()
	}
	s.log.InfoContext(r.Context(), "snapshot recebido", "agent_id", agent.ID, "snapshot_id", resp.SnapshotID, "duplicate", resp.Duplicate, "bytes", len(body))
	writeJSON(w, status, resp)
}

// readBody lê o corpo, descomprimindo se vier em gzip, com limites de
// tamanho antes e depois da descompressão.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	var rd io.Reader = http.MaxBytesReader(w, r.Body, maxSnapshotWire)
	switch enc := r.Header.Get("Content-Encoding"); enc {
	case "gzip":
		gz, err := gzip.NewReader(rd)
		if err != nil {
			return nil, errorf(http.StatusBadRequest, "gzip inválido: %s", err)
		}
		defer gz.Close()
		rd = gz
	case "", "identity":
	default:
		return nil, errorf(http.StatusUnsupportedMediaType, "Content-Encoding não suportado: %s", enc)
	}
	body, err := io.ReadAll(io.LimitReader(rd, maxSnapshotBody+1))
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &mbe), len(body) > maxSnapshotBody:
		return nil, errorCode(http.StatusRequestEntityTooLarge, wire.ErrorCodeTooLarge, "snapshot grande demais")
	case err != nil:
		return nil, errorf(http.StatusBadRequest, "corpo ilegível: %s", err)
	}
	return body, nil
}

// validate confere o corpo contra o JSON Schema e as regras de pkg/snapshot.
func (s *Server) validate(body []byte) (*snapshot.Snapshot, error) {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return nil, errorf(http.StatusBadRequest, "JSON inválido: %s", err)
	}
	// Só existe a v1; quando houver outra, o schema passa a ser escolhido
	// pelo schema_version do corpo.
	if err := s.schema.Validate(inst); err != nil {
		return nil, errorf(http.StatusUnprocessableEntity, "snapshot fora do schema: %s", err)
	}
	// O jsonb do PostgreSQL não guarda \u0000. Recusar aqui (422) manda o
	// snapshot para o rejected/ do agente; deixar o INSERT falhar daria 500,
	// e o agente tentaria de novo para sempre, travando a fila.
	if hasNULEscape(body) {
		return nil, errorf(http.StatusUnprocessableEntity, "snapshot com \\u0000 num texto, que o PostgreSQL não guarda")
	}
	var snap snapshot.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return nil, errorf(http.StatusBadRequest, "JSON inválido: %s", err)
	}
	if err := snap.Validate(); err != nil {
		return nil, errorf(http.StatusUnprocessableEntity, "%s", err)
	}
	return &snap, nil
}

// hasNULEscape diz se o JSON tem um \u0000 de verdade, e não o texto
// "\\u0000" (barra escapada seguida de u0000).
func hasNULEscape(body []byte) bool {
	const esc = `\u0000`
	for i := 0; ; {
		j := bytes.Index(body[i:], []byte(esc))
		if j < 0 {
			return false
		}
		j += i
		n := 0 // barras antes desta
		for k := j - 1; k >= 0 && body[k] == '\\'; k-- {
			n++
		}
		if n%2 == 0 {
			return true
		}
		i = j + len(esc)
	}
}
