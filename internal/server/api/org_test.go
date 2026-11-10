package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/athosbes/invenire/internal/server/api"
	"github.com/athosbes/invenire/internal/server/store"
)

// kinds devolve os tipos de evento do change_log do tenant, em ordem.
func (e *env) kinds(tenant uuid.UUID) []string {
	e.t.Helper()
	rows, err := e.q.ListChangesAfter(context.Background(), store.ListChangesAfterParams{TenantID: tenant, Limit: 1000})
	if err != nil {
		e.t.Fatal(err)
	}
	var k []string
	for _, r := range rows {
		k = append(k, r.Kind)
	}
	return k
}

func count(list []string, kind string) int {
	n := 0
	for _, k := range list {
		if k == kind {
			n++
		}
	}
	return n
}

func TestUnits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.user(e.tenant, "admin", "admin")
	reader := e.user(e.tenant, "leitor", "read")
	other, _ := e.q.CreateTenant(ctx, "outro")
	stranger := e.user(other, "admin-de-outro", "admin")

	create := func(token string, req api.UnitCreate) (int, api.Unit) {
		t.Helper()
		var resp api.UnitResponse
		status := e.call("POST", "/v1/units", token, req, &resp)
		return status, resp.Unit
	}
	if status, _ := create(reader, api.UnitCreate{Name: "Saúde"}); status != http.StatusForbidden {
		t.Fatalf("leitor criou: %d", status)
	}
	status, saude := create(admin, api.UnitCreate{Kind: "secretaria", Name: "Saúde"})
	if status != http.StatusCreated || saude.Path != "Saúde" || saude.ParentID != nil {
		t.Fatalf("raiz: %d %+v", status, saude)
	}
	status, ubs := create(admin, api.UnitCreate{ParentPath: "Saúde", Kind: "ubs", Name: "UBS Centro"})
	if status != http.StatusCreated || ubs.Path != "Saúde/UBS Centro" || ubs.ParentID == nil || *ubs.ParentID != saude.ID {
		t.Fatalf("filha por caminho: %d %+v", status, ubs)
	}
	status, recepcao := create(admin, api.UnitCreate{ParentID: &ubs.ID, Kind: "setor", Name: "Recepção"})
	if status != http.StatusCreated || recepcao.Path != "Saúde/UBS Centro/Recepção" {
		t.Fatalf("neta por id: %d %+v", status, recepcao)
	}
	_, educacao := create(admin, api.UnitCreate{Kind: "secretaria", Name: "Educação"})

	for name, req := range map[string]api.UnitCreate{
		"repetida":          {Name: "Saúde"},
		"barra no nome":     {Name: "TI/Redes"},
		"espaço nas pontas": {Name: " TI"},
		"vazia":             {Name: ""},
	} {
		want := http.StatusBadRequest
		if name == "repetida" {
			want = http.StatusConflict
		}
		if status, _ := create(admin, req); status != want {
			t.Errorf("%s: %d, queria %d", name, status, want)
		}
	}
	if status, _ := create(admin, api.UnitCreate{ParentPath: "Saúde/Nada", Name: "X"}); status != http.StatusNotFound {
		t.Errorf("mãe inexistente: %d", status)
	}
	// A unidade de um tenant não existe para o outro.
	if status, _ := create(stranger, api.UnitCreate{ParentID: &saude.ID, Name: "Invasão"}); status != http.StatusNotFound {
		t.Errorf("mãe de outro tenant: %d", status)
	}
	// O mesmo nome em níveis diferentes é permitido.
	if status, _ := create(admin, api.UnitCreate{ParentID: &educacao.ID, Name: "UBS Centro"}); status != http.StatusCreated {
		t.Errorf("mesmo nome em outro nível: %d", status)
	}

	var list api.UnitList
	if status := e.call("GET", "/v1/units", reader, nil, &list); status != http.StatusOK {
		t.Fatalf("lista: %d", status)
	}
	var paths []string
	for _, u := range list.Items {
		paths = append(paths, u.Path)
	}
	want := []string{"Educação", "Educação/UBS Centro", "Saúde", "Saúde/UBS Centro", "Saúde/UBS Centro/Recepção"}
	if len(paths) != len(want) {
		t.Fatalf("árvore: %q", paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("árvore: %q", paths)
		}
	}
	var otherList api.UnitList
	if e.call("GET", "/v1/units", stranger, nil, &otherList); len(otherList.Items) != 0 {
		t.Fatalf("outro tenant vê unidades: %+v", otherList.Items)
	}

	// Mover a Saúde para dentro da Recepção criaria um ciclo.
	patch := func(token string, id uuid.UUID, body any) (int, api.Unit) {
		t.Helper()
		var resp api.UnitResponse
		status := e.call("PATCH", "/v1/units/"+id.String(), token, body, &resp)
		return status, resp.Unit
	}
	if status, _ := patch(admin, saude.ID, map[string]any{"parent_id": recepcao.ID}); status != http.StatusConflict {
		t.Fatalf("ciclo: %d", status)
	}
	if status, _ := patch(admin, saude.ID, map[string]any{"parent_id": saude.ID}); status != http.StatusConflict {
		t.Fatalf("mãe de si mesma: %d", status)
	}
	// Recepção vai para a raiz e volta com outro nome.
	if status, u := patch(admin, recepcao.ID, map[string]any{"parent_id": nil}); status != http.StatusOK || u.Path != "Recepção" || u.ParentID != nil {
		t.Fatalf("para a raiz: %d %+v", status, u)
	}
	if status, u := patch(admin, recepcao.ID, map[string]any{"parent_id": ubs.ID, "name": "Atendimento"}); status != http.StatusOK || u.Path != "Saúde/UBS Centro/Atendimento" {
		t.Fatalf("volta renomeada: %d %+v", status, u)
	}
	if status, _ := patch(admin, educacao.ID, map[string]any{"name": "Saúde"}); status != http.StatusConflict {
		t.Fatalf("renomear para nome repetido: %d", status)
	}
	if status, _ := patch(admin, educacao.ID, map[string]any{}); status != http.StatusBadRequest {
		t.Fatalf("nada a mudar: %d", status)
	}
	if status, _ := patch(stranger, educacao.ID, map[string]any{"name": "Minha"}); status != http.StatusNotFound {
		t.Fatalf("outro tenant renomeou: %d", status)
	}

	// Apagar: só unidade vazia.
	if status := e.call("DELETE", "/v1/units/"+ubs.ID.String(), admin, nil, nil); status != http.StatusConflict {
		t.Fatalf("apagou unidade com filha: %d", status)
	}
	if status := e.call("DELETE", "/v1/units/"+recepcao.ID.String(), admin, nil, nil); status != http.StatusOK {
		t.Fatalf("apagar folha: %d", status)
	}

	k := e.kinds(e.tenant.ID)
	if count(k, "unit_created") != 5 || count(k, "unit_updated") != 2 || count(k, "unit_deleted") != 1 {
		t.Fatalf("change_log: %q", k)
	}
	if len(e.kinds(other.ID)) != 0 {
		t.Fatalf("change_log do outro tenant: %q", e.kinds(other.ID))
	}
}

func TestAssetUnitAndLabels(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.user(e.tenant, "admin", "admin")
	reader := e.user(e.tenant, "leitor", "read")
	other, _ := e.q.CreateTenant(ctx, "outro")
	stranger := e.user(other, "admin-de-outro", "admin")

	ubs, _, err := api.CreateUnit(ctx, e.q, e.tenant.ID, nil, nil, "ubs", "UBS Centro")
	if err != nil {
		t.Fatal(err)
	}
	dtic, _, err := api.CreateUnit(ctx, e.q, e.tenant.ID, nil, nil, "setor", "DTIC")
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := api.CreateUnit(ctx, e.q, other.ID, nil, nil, "", "Deles")
	if err != nil {
		t.Fatal(err)
	}

	// O token leva a unidade: a máquina nasce nela.
	tok, _, err := api.NewEnrollmentToken(ctx, e.q, e.tenant.ID, &ubs.ID, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	status, enrolled, _ := e.enroll(tok, testHost)
	if status != http.StatusCreated {
		t.Fatalf("cadastro: %d", status)
	}
	asset, _ := e.q.GetAsset(ctx, enrolled.AssetID)
	if asset.UnitID == nil || *asset.UnitID != ubs.ID {
		t.Fatalf("unidade depois do cadastro: %v", asset.UnitID)
	}
	// Token da unidade de outro tenant não existe: o banco recusa.
	if _, _, err := api.NewEnrollmentToken(ctx, e.q, e.tenant.ID, &foreign.ID, "", time.Hour); err == nil {
		t.Fatal("token com unidade de outro tenant")
	}

	path := "/v1/assets/" + enrolled.AssetID.String() + "/unit"
	var moved api.AssetUnitResponse
	if status := e.call("PUT", path, reader, api.AssetUnitRequest{UnitID: &dtic.ID}, nil); status != http.StatusForbidden {
		t.Fatalf("leitor moveu: %d", status)
	}
	if status := e.call("PUT", path, stranger, api.AssetUnitRequest{UnitID: &foreign.ID}, nil); status != http.StatusNotFound {
		t.Fatalf("outro tenant moveu: %d", status)
	}
	if status := e.call("PUT", path, admin, api.AssetUnitRequest{UnitID: &foreign.ID}, nil); status != http.StatusNotFound {
		t.Fatalf("unidade de outro tenant: %d", status)
	}
	if status := e.call("PUT", path, admin, api.AssetUnitRequest{UnitID: &dtic.ID}, &moved); status != http.StatusOK ||
		moved.PreviousUnitID == nil || *moved.PreviousUnitID != ubs.ID || *moved.UnitID != dtic.ID {
		t.Fatalf("mover: %d %+v", status, moved)
	}
	// Recadastrar com token de outra unidade não tira a máquina do DTIC.
	tok2, _, _ := api.NewEnrollmentToken(ctx, e.q, e.tenant.ID, &ubs.ID, "", time.Hour)
	if status, _, _ := e.enroll(tok2, testHost); status != http.StatusCreated {
		t.Fatalf("recadastro: %d", status)
	}
	if asset, _ = e.q.GetAsset(ctx, enrolled.AssetID); *asset.UnitID != dtic.ID {
		t.Fatalf("recadastro moveu a máquina para %v", asset.UnitID)
	}
	// Mesma unidade de novo: nada muda, nada é registrado.
	before := len(e.kinds(e.tenant.ID))
	e.call("PUT", path, admin, api.AssetUnitRequest{UnitID: &dtic.ID}, nil)
	if len(e.kinds(e.tenant.ID)) != before {
		t.Fatal("mover para a mesma unidade registrou evento")
	}
	// Unidade com ativo não pode ser apagada.
	if status := e.call("DELETE", "/v1/units/"+dtic.ID.String(), admin, nil, nil); status != http.StatusConflict {
		t.Fatalf("apagou unidade com ativo: %d", status)
	}
	// Unidade com token de cadastro válido também não (os dois de antes já
	// foram usados).
	if _, _, err := api.NewEnrollmentToken(ctx, e.q, e.tenant.ID, &ubs.ID, "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if status := e.call("DELETE", "/v1/units/"+ubs.ID.String(), admin, nil, nil); status != http.StatusConflict {
		t.Fatalf("apagou unidade com token válido: %d", status)
	}
	if status := e.call("PUT", path, admin, map[string]any{"unit_id": nil}, &moved); status != http.StatusOK || moved.UnitID != nil {
		t.Fatalf("tirar da unidade: %d %+v", status, moved)
	}

	// Etiquetas.
	lpath := "/v1/assets/" + enrolled.AssetID.String() + "/labels/"
	var lr api.LabelResponse
	if status := e.call("PUT", lpath+"ambiente", admin, api.LabelRequest{Value: "produção"}, &lr); status != http.StatusOK || *lr.Value != "produção" || lr.PreviousValue != nil {
		t.Fatalf("criar etiqueta: %d %+v", status, lr)
	}
	if status := e.call("PUT", lpath+"ambiente", admin, api.LabelRequest{Value: "homologação"}, &lr); status != http.StatusOK || *lr.PreviousValue != "produção" {
		t.Fatalf("trocar etiqueta: %d %+v", status, lr)
	}
	e.call("PUT", lpath+"dados", admin, api.LabelRequest{Value: "pessoais"}, nil)
	for _, bad := range []string{"Ambiente", "_x", "com%20espaço"} {
		if status := e.call("PUT", lpath+bad, admin, api.LabelRequest{Value: "x"}, nil); status != http.StatusBadRequest {
			t.Errorf("chave %q: %d", bad, status)
		}
	}
	if status := e.call("PUT", lpath+"ambiente", reader, api.LabelRequest{Value: "x"}, nil); status != http.StatusForbidden {
		t.Fatalf("leitor etiquetou: %d", status)
	}
	if status := e.call("PUT", lpath+"ambiente", stranger, api.LabelRequest{Value: "x"}, nil); status != http.StatusNotFound {
		t.Fatalf("outro tenant etiquetou: %d", status)
	}
	var labels api.AssetLabels
	if status := e.call("GET", "/v1/assets/"+enrolled.AssetID.String()+"/labels", reader, nil, &labels); status != http.StatusOK ||
		len(labels.Labels) != 2 || labels.Labels["ambiente"] != "homologação" || labels.Labels["dados"] != "pessoais" {
		t.Fatalf("listar etiquetas: %d %+v", status, labels)
	}
	if status := e.call("GET", "/v1/assets/"+enrolled.AssetID.String()+"/labels", stranger, nil, nil); status != http.StatusNotFound {
		t.Fatalf("outro tenant leu etiquetas: %d", status)
	}
	if status := e.call("DELETE", lpath+"dados", admin, nil, &lr); status != http.StatusOK || lr.Value != nil || *lr.PreviousValue != "pessoais" {
		t.Fatalf("remover etiqueta: %d %+v", status, lr)
	}
	if status := e.call("DELETE", lpath+"dados", admin, nil, nil); status != http.StatusNotFound {
		t.Fatalf("remover de novo: %d", status)
	}

	k := e.kinds(e.tenant.ID)
	if count(k, "asset_moved") != 3 || count(k, "label_set") != 3 || count(k, "label_removed") != 1 {
		t.Fatalf("change_log: %q", k)
	}

	// O banco também garante: ativo não entra em unidade de outro tenant.
	if _, err := e.pool.Exec(ctx, "UPDATE asset SET unit_id = $1 WHERE id = $2", foreign.ID, enrolled.AssetID); err == nil {
		t.Fatal("banco aceitou unidade de outro tenant")
	}
}
