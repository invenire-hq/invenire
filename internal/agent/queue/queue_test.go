// SPDX-License-Identifier: AGPL-3.0-only

package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func put(t *testing.T, q *Queue, body string, at time.Time) []Item {
	t.Helper()
	h := sha256.Sum256([]byte(body))
	dropped, err := q.Put([]byte(body), hex.EncodeToString(h[:]), at)
	if err != nil {
		t.Fatal(err)
	}
	return dropped
}

func TestQueueOrderAndLimit(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	// Fora de ordem de propósito: a fila ordena pelo instante da coleta.
	put(t, q, "b", t0.Add(time.Hour))
	put(t, q, "a", t0)
	dropped := put(t, q, "c", t0.Add(2*time.Hour))
	if len(dropped) != 1 {
		t.Fatalf("esperava descartar 1, descartou %d", len(dropped))
	}

	items, err := q.List()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		b, err := q.Read(it)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(b))
	}
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("fila: %v", got)
	}

	if err := q.Done(items[0]); err != nil {
		t.Fatal(err)
	}
	if err := q.Reject(items[1], "fora do schema"); err != nil {
		t.Fatal(err)
	}
	if items, _ := q.List(); len(items) != 0 {
		t.Fatalf("fila devia estar vazia: %v", items)
	}
	if _, err := os.Stat(filepath.Join(dir, rejectedDir, filepath.Base(items[1].Path)+".reason.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestQueueDetectsCorruption(t *testing.T) {
	q, err := Open(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	put(t, q, "original", time.Now())
	items, _ := q.List()
	os.WriteFile(items[0].Path, []byte("alterado"), 0o600)
	if _, err := q.Read(items[0]); err == nil {
		t.Fatal("leu arquivo alterado sem erro")
	}
}
