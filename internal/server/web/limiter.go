// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"sync"
	"time"
)

// limiter conta falhas por chave (IP, e-mail) numa janela. Fica na memória
// do processo: reiniciar o servidor zera as contas, o que não ajuda quem
// tenta adivinhar uma senha (o reinício não está nas mãos dele).
type limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	hits map[string][]time.Time
	seen int
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, now: time.Now, hits: map[string][]time.Time{}}
}

func (l *limiter) recent(key string, now time.Time) []time.Time {
	h := l.hits[key]
	i := 0
	for i < len(h) && now.Sub(h[i]) > l.window {
		i++
	}
	h = h[i:]
	if len(h) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = h
	}
	return h
}

// allow diz se a chave ainda pode tentar.
func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key, l.now())) < l.max
}

// fail conta uma falha da chave.
func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.hits[key] = append(l.recent(key, now), now)
	// De vez em quando, limpa as chaves que já saíram da janela.
	if l.seen++; l.seen%1000 == 0 {
		for k := range l.hits {
			l.recent(k, now)
		}
	}
}

// reset esquece as falhas da chave (login certo).
func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}
