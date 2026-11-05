package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/athosbes/invenire/internal/server/api"
	"github.com/athosbes/invenire/internal/server/ingest"
	"github.com/athosbes/invenire/internal/server/normalize"
	"github.com/athosbes/invenire/internal/server/store"
	"github.com/athosbes/invenire/pkg/snapshot"
)

// user cria um ator com os escopos e devolve o token de API dele.
func (e *env) user(tenant store.Tenant, name string, scopes ...string) string {
	e.t.Helper()
	ctx := context.Background()
	a, err := e.q.CreateActor(ctx, store.CreateActorParams{TenantID: tenant.ID, Kind: "human", Name: name, Scopes: scopes})
	if err != nil {
		e.t.Fatal(err)
	}
	tok, _, err := api.NewAPIToken(ctx, e.q, a.ID, "", time.Hour)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func (e *env) call(method, path, token string, body any, out any) int {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := e.client(nil).Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestUnmappedAndManualMapping(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rules, err := normalize.Default()
	if err != nil {
		t.Fatal(err)
	}
	e.api.Suggest = rules.Evaluate
	worker := ingest.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)), rules)

	// Um ativo com Chrome (catálogo), Cursor e um nome que nenhuma regra
	// entende.
	_, enrolled, cert := e.enroll(e.token(), testHost)
	s := newSnapshot(testHost, "")
	s.AgentID = enrolled.AgentID.String()
	s.Software = []snapshot.Software{
		{Source: "win_registry", Name: "Google Chrome", Publisher: "Google LLC", Version: "130.0.6723.70"},
		{Source: "win_registry", Name: "Cursor (User)", Publisher: "Anysphere", Version: "1.7.0"},
		{Source: "win_registry", Name: "Sistema Interno 2.1", Version: "2.1"},
	}
	body, sum, _ := snapshot.Marshal(s)
	if status, _, msg := e.send(e.client(cert), body, sum); status != http.StatusAccepted {
		t.Fatalf("envio: %d %s", status, msg)
	}
	if n, err := worker.Drain(ctx); err != nil || n != 1 {
		t.Fatalf("ingestão: %d %v", n, err)
	}

	reader := e.user(e.tenant, "leitor", "read")
	admin := e.user(e.tenant, "admin", "admin")
	other, err := e.q.CreateTenant(ctx, "outro")
	if err != nil {
		t.Fatal(err)
	}
	stranger := e.user(other, "admin-de-outro", "admin")

	if status := e.call("GET", "/v1/software/unmapped", "", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("sem token: %d", status)
	}
	if status := e.call("GET", "/v1/software/unmapped", "inva_falso", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("token falso: %d", status)
	}

	var list api.UnmappedList
	if status := e.call("GET", "/v1/software/unmapped", reader, nil, &list); status != http.StatusOK || len(list.Items) != 2 || list.AsOf == 0 {
		t.Fatalf("lista: %d %+v", status, list)
	}
	var cursor api.UnmappedItem
	for _, it := range list.Items {
		if it.Collected.Name == "Cursor (User)" {
			cursor = it
		}
	}
	if cursor.AssetCount != 1 || cursor.Suggestion == nil || cursor.Suggestion.Product != "Cursor" || cursor.Suggestion.Confidence >= 0.9 {
		t.Fatalf("item do Cursor: %+v (sugestão %+v)", cursor, cursor.Suggestion)
	}

	// Paginação por cursor.
	var p1, p2 api.UnmappedList
	e.call("GET", "/v1/software/unmapped?limit=1", reader, nil, &p1)
	if len(p1.Items) != 1 || p1.NextCursor == "" {
		t.Fatalf("página 1: %+v", p1)
	}
	e.call("GET", "/v1/software/unmapped?limit=1&cursor="+p1.NextCursor, reader, nil, &p2)
	if len(p2.Items) != 1 || p2.Items[0].ID == p1.Items[0].ID {
		t.Fatalf("página 2: %+v", p2)
	}

	path := "/v1/software/raw/" + cursor.ID.String()
	req := api.MapRequest{Vendor: "Anysphere", Product: "Cursor"}
	if status := e.call("PUT", path, reader, req, nil); status != http.StatusForbidden {
		t.Fatalf("leitor mapeou: %d", status)
	}
	if status := e.call("PUT", path, stranger, req, nil); status != http.StatusNotFound {
		t.Fatalf("outro tenant mapeou: %d", status)
	}
	if status := e.call("PUT", path, admin, api.MapRequest{Vendor: "X"}, nil); status != http.StatusBadRequest {
		t.Fatalf("sem produto: %d", status)
	}
	var mapped api.MapResponse
	if status := e.call("PUT", path, admin, req, &mapped); status != http.StatusOK || mapped.AssetsToApply != 1 || mapped.AsOf <= list.AsOf {
		t.Fatalf("mapear: %d %+v", status, mapped)
	}

	// As instalações do ativo são recalculadas sem novo snapshot.
	if n, err := worker.Drain(ctx); err != nil || n != 1 {
		t.Fatalf("reaplicação: %d %v", n, err)
	}
	rows, err := e.q.ListInstallationsWithSoftware(ctx, enrolled.AssetID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range rows {
		names = append(names, r.Product+" "+r.Version)
	}
	if len(rows) != 2 || names[0] != "Chrome 130.0.6723.70" || names[1] != "Cursor 1.7.0" {
		t.Fatalf("instalações: %q", names)
	}
	e.call("GET", "/v1/software/unmapped", reader, nil, &list)
	if len(list.Items) != 1 || list.Items[0].Collected.Name != "Sistema Interno 2.1" {
		t.Fatalf("lista depois do mapeamento: %+v", list.Items)
	}

	// Mapeamento manual não é desfeito pelas regras.
	if n, err := worker.RemapAll(ctx); err != nil || n != 0 {
		t.Fatalf("RemapAll mexeu em mapeamento manual: %d %v", n, err)
	}
}
