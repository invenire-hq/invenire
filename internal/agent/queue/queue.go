// SPDX-License-Identifier: AGPL-3.0-only

// Package queue é a fila local de snapshots em disco. Cada snapshot é um
// arquivo com os bytes exatos que serão enviados, nomeado pelo instante da
// coleta e pelo SHA-256, para a ordem e a integridade virem do próprio nome.
package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/invenire-hq/invenire/internal/agent/state"
)

const (
	ext         = ".json"
	rejectedDir = "rejected"
	// Formato de tempo que ordena lexicograficamente.
	stamp = "20060102T150405.000000000Z"
)

// Queue é uma fila em disco.
type Queue struct {
	dir string
	max int
}

// Item é um snapshot na fila.
type Item struct {
	Path string
	Sum  string
}

// Open abre a fila em dir, guardando no máximo max snapshots.
func Open(dir string, max int) (*Queue, error) {
	if err := os.MkdirAll(filepath.Join(dir, rejectedDir), 0o700); err != nil {
		return nil, err
	}
	return &Queue{dir: dir, max: max}, nil
}

// Put grava um snapshot na fila. Se ela passar de max, os mais antigos são
// descartados e devolvidos em dropped (o servidor verá o buraco no
// encadeamento como snapshot_chain_broken).
func (q *Queue) Put(body []byte, sum string, collectedAt time.Time) (dropped []Item, err error) {
	name := collectedAt.UTC().Format(stamp) + "-" + sum + ext
	if err := state.WriteAtomic(filepath.Join(q.dir, name), body); err != nil {
		return nil, err
	}
	items, err := q.List()
	if err != nil {
		return nil, err
	}
	for len(items) > q.max {
		if err := os.Remove(items[0].Path); err != nil {
			return dropped, err
		}
		dropped = append(dropped, items[0])
		items = items[1:]
	}
	return dropped, nil
}

// List devolve os snapshots da fila, do mais antigo para o mais novo.
func (q *Queue) List() ([]Item, error) {
	entries, err := os.ReadDir(q.dir)
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ext) {
			continue
		}
		_, sum, ok := strings.Cut(strings.TrimSuffix(name, ext), "-")
		if !ok || len(sum) != 64 {
			continue
		}
		items = append(items, Item{Path: filepath.Join(q.dir, name), Sum: sum})
	}
	slices.SortFunc(items, func(a, b Item) int { return strings.Compare(a.Path, b.Path) })
	return items, nil
}

// Read lê o snapshot e confere o hash do nome contra o conteúdo.
func (q *Queue) Read(it Item) ([]byte, error) {
	b, err := os.ReadFile(it.Path)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != it.Sum {
		return nil, fmt.Errorf("%s: conteúdo não bate com o SHA-256 do nome", filepath.Base(it.Path))
	}
	return b, nil
}

// Done tira da fila um snapshot entregue.
func (q *Queue) Done(it Item) error {
	err := os.Remove(it.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Reject move para rejected/ um snapshot que o servidor recusou de vez, junto
// com o motivo, para análise. Ele não volta a ser enviado.
func (q *Queue) Reject(it Item, reason string) error {
	dst := filepath.Join(q.dir, rejectedDir, filepath.Base(it.Path))
	if err := os.Rename(it.Path, dst); err != nil {
		return err
	}
	return state.WriteAtomic(dst+".reason.txt", []byte(reason+"\n"))
}
