// SPDX-License-Identifier: AGPL-3.0-only

package runner_test

import (
	"context"
	"testing"
	"time"

	"github.com/invenire-hq/invenire/internal/agent/runner"
	"github.com/invenire-hq/invenire/internal/agent/state"
)

// O agente renova o crachá quando ele está perto de vencer, mantém a chave e
// segue entregando com o novo.
func TestRenewIfDue(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	dir, err := state.OpenUnrestricted(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Enroll(ctx, dir, e.enrollOpts(e.token(t))); err != nil {
		t.Fatal(err)
	}
	host := thisHost
	a := &runner.Agent{Dir: dir, Version: "test", Log: quiet, Collect: collector(&host)}
	if err := a.Open(); err != nil {
		t.Fatal(err)
	}
	before, _, err := dir.Identity()
	if err != nil {
		t.Fatal(err)
	}

	// Crachá recém-emitido, de dois anos: ainda não é hora.
	if renewed, err := a.RenewIfDue(ctx); err != nil || renewed {
		t.Fatalf("renovou cedo demais: %v %v", renewed, err)
	}

	// Servidor fora do ar na hora de renovar: erro, e o crachá continua o mesmo.
	a.RenewBefore = 3 * 365 * 24 * time.Hour
	e.down.Store(true)
	if renewed, err := a.RenewIfDue(ctx); err == nil || renewed {
		t.Fatalf("renovação com servidor fora: %v %v", renewed, err)
	}
	if same, _, _ := dir.Identity(); !same.Equal(before) {
		t.Fatal("o crachá mudou sem renovação")
	}
	e.down.Store(false)

	// O emitido agora vence depois do atual: o backdate dos dois é igual, mas
	// o relógio andou. Um segundo de folga para a validade (em segundos) mudar.
	time.Sleep(1100 * time.Millisecond)
	if renewed, err := a.RenewIfDue(ctx); err != nil || !renewed {
		t.Fatalf("renovação: %v %v", renewed, err)
	}
	after, _, err := dir.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if after.Equal(before) || !after.NotAfter.After(before.NotAfter) || after.Subject.CommonName != before.Subject.CommonName {
		t.Fatalf("crachá depois de renovar: %s até %s", after.Subject.CommonName, after.NotAfter)
	}

	// Entrega com o crachá novo.
	if _, err := a.Enqueue(ctx); err != nil {
		t.Fatal(err)
	}
	if sent, err := a.Flush(ctx); err != nil || sent != 1 {
		t.Fatalf("entrega com o crachá novo: %d %v", sent, err)
	}
}
