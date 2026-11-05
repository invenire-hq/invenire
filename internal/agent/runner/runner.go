// Package runner junta coleta, fila e envio: é o que os comandos enroll e
// run do invenire-agent executam.
package runner

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"net/url"
	"time"

	"github.com/athosbes/invenire/internal/agent/collect"
	"github.com/athosbes/invenire/internal/agent/queue"
	"github.com/athosbes/invenire/internal/agent/state"
	"github.com/athosbes/invenire/internal/agent/transport"
	"github.com/athosbes/invenire/internal/wire"
	"github.com/athosbes/invenire/pkg/snapshot"
)

// ErrRevoked indica que o servidor não reconhece mais o certificado do agente.
var ErrRevoked = errors.New("o servidor recusou o certificado do agente (revogado ou desconhecido): é preciso cadastrar de novo")

// EnrollOptions são os parâmetros do cadastro.
type EnrollOptions struct {
	ServerURL    string
	Token        string
	CAPin        []byte // SHA-256 do certificado da CA
	AgentVersion string
	Force        bool // cadastrar de novo mesmo já havendo cadastro
	// Identity coleta host e SO; nulo usa collect.Identity.
	Identity func(context.Context) (snapshot.Host, snapshot.OS, error)
}

// Enroll gera a chave do agente, faz o cadastro e grava o resultado em dir.
// A chave nunca sai da máquina: o servidor só vê o CSR.
func Enroll(ctx context.Context, dir state.Dir, opts EnrollOptions) (state.Config, error) {
	if dir.Enrolled() && !opts.Force {
		return state.Config{}, errors.New("o agente já está cadastrado neste diretório (use --force para cadastrar de novo)")
	}
	identity := opts.Identity
	if identity == nil {
		identity = collect.Identity
	}
	host, os, err := identity(ctx)
	if snapshot.Fingerprint(host) == "" {
		return state.Config{}, fmt.Errorf("não foi possível identificar a máquina: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return state.Config{}, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return state.Config{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return state.Config{}, err
	}

	u, err := url.Parse(opts.ServerURL)
	if err != nil {
		return state.Config{}, fmt.Errorf("URL do servidor: %w", err)
	}
	client, err := transport.NewClient(opts.ServerURL, transport.PinnedTLS(opts.CAPin, u.Hostname()))
	if err != nil {
		return state.Config{}, err
	}
	resp, err := client.Enroll(ctx, wire.EnrollRequest{
		Token:        opts.Token,
		CSR:          string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})),
		AgentVersion: opts.AgentVersion,
		Host:         host,
		OS:           os,
	})
	if err != nil {
		return state.Config{}, fmt.Errorf("cadastro: %w", err)
	}
	// A CA que vem na resposta é a que o agente vai usar daqui em diante:
	// precisa ser a mesma do pin.
	if b, _ := pem.Decode([]byte(resp.CA)); b == nil || !transport.PinMatches(b.Bytes, opts.CAPin) {
		return state.Config{}, errors.New("a CA devolvida no cadastro não confere com o pin")
	}

	cfg := state.Config{ServerURL: opts.ServerURL, AgentID: resp.AgentID.String(), AssetID: resp.AssetID.String()}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := dir.SaveEnrollment(cfg, keyPEM, []byte(resp.Certificate), []byte(resp.CA)); err != nil {
		return state.Config{}, err
	}
	// Cadastro novo começa um encadeamento novo.
	return cfg, dir.SetLastSum("")
}

// Agent é o agente cadastrado.
type Agent struct {
	Dir     state.Dir
	Version string
	Log     *slog.Logger
	// MaxQueue é quantos snapshots a fila guarda sem conseguir enviar.
	MaxQueue int
	// Collect faz a coleta completa; nulo usa collect.Collect.
	Collect func(ctx context.Context, agentVersion string) *snapshot.Snapshot

	cfg    state.Config
	client *transport.Client
	queue  *queue.Queue
}

// Open carrega o cadastro de dir.
func (a *Agent) Open() error {
	cfg, err := a.Dir.Config()
	if err != nil {
		return err
	}
	tlsCfg, err := a.Dir.TLSConfig()
	if err != nil {
		return err
	}
	client, err := transport.NewClient(cfg.ServerURL, tlsCfg)
	if err != nil {
		return err
	}
	if a.MaxQueue <= 0 {
		a.MaxQueue = 100
	}
	q, err := queue.Open(a.Dir.QueueDir(), a.MaxQueue)
	if err != nil {
		return err
	}
	if a.Collect == nil {
		a.Collect = collect.Collect
	}
	a.cfg, a.client, a.queue = cfg, client, q
	return nil
}

// Enqueue coleta um snapshot, encadeia no anterior e o põe na fila.
func (a *Agent) Enqueue(ctx context.Context) (string, error) {
	s := a.Collect(ctx, a.Version)
	for _, e := range s.CollectorErrors {
		a.Log.WarnContext(ctx, "coletor falhou", "collector", e.Collector, "err", e.Error)
	}
	s.AgentID = a.cfg.AgentID
	prev, err := a.Dir.LastSum()
	if err != nil {
		return "", err
	}
	s.PrevSHA256 = prev
	body, sum, err := snapshot.Marshal(s)
	if err != nil {
		return "", err
	}
	dropped, err := a.queue.Put(body, sum, s.CollectedAt)
	if err != nil {
		return "", err
	}
	for _, d := range dropped {
		a.Log.WarnContext(ctx, "fila cheia: snapshot mais antigo descartado", "sha256", d.Sum)
	}
	if err := a.Dir.SetLastSum(sum); err != nil {
		return "", err
	}
	a.Log.InfoContext(ctx, "snapshot na fila", "sha256", sum, "software", len(s.Software), "bytes", len(body))
	return sum, nil
}

// Flush envia a fila do mais antigo para o mais novo e para no primeiro erro
// momentâneo. Snapshots que o servidor recusa de vez vão para rejected/.
func (a *Agent) Flush(ctx context.Context) (sent int, err error) {
	items, err := a.queue.List()
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		body, err := a.queue.Read(it)
		if err != nil {
			a.Log.ErrorContext(ctx, "snapshot corrompido na fila", "err", err)
			if err := a.queue.Reject(it, err.Error()); err != nil {
				return sent, err
			}
			continue
		}
		resp, err := a.client.SendSnapshot(ctx, body, it.Sum)
		var he *transport.HTTPError
		switch {
		case err == nil:
		case errors.As(err, &he) && he.Status == 401:
			return sent, ErrRevoked
		case errors.As(err, &he) && he.Permanent():
			a.Log.ErrorContext(ctx, "servidor recusou o snapshot", "sha256", it.Sum, "err", err)
			if err := a.queue.Reject(it, err.Error()); err != nil {
				return sent, err
			}
			continue
		default:
			return sent, err
		}
		if err := a.queue.Done(it); err != nil {
			return sent, err
		}
		sent++
		a.Log.InfoContext(ctx, "snapshot entregue", "sha256", it.Sum, "snapshot_id", resp.SnapshotID, "duplicate", resp.Duplicate)
	}
	return sent, nil
}

// Pending diz quantos snapshots esperam na fila.
func (a *Agent) Pending() (int, error) {
	items, err := a.queue.List()
	return len(items), err
}

// Run coleta a cada interval e tenta esvaziar a fila entre as coletas, com
// espera exponencial quando o servidor não responde. Só termina com o ctx
// ou se o certificado for recusado.
func (a *Agent) Run(ctx context.Context, interval time.Duration) error {
	const minBackoff, maxBackoff = 30 * time.Second, 30 * time.Minute
	for {
		// Até 10% de variação, para as máquinas não coletarem todas juntas.
		next := time.Now().Add(interval + mrand.N(interval/10+1))
		if _, err := a.Enqueue(ctx); err != nil {
			a.Log.ErrorContext(ctx, "coleta falhou", "err", err)
		}

		backoff := minBackoff
		for {
			_, err := a.Flush(ctx)
			if err == nil || errors.Is(err, ErrRevoked) {
				if err != nil {
					return err
				}
				break
			}
			wait := min(backoff, time.Until(next))
			a.Log.WarnContext(ctx, "envio falhou; snapshot fica na fila", "err", err, "retry_in", wait.Round(time.Second))
			if wait <= 0 || !sleep(ctx, wait) {
				break
			}
			backoff = min(backoff*2, maxBackoff)
		}
		if !sleep(ctx, time.Until(next)) {
			return ctx.Err()
		}
	}
}

// sleep espera d ou até o ctx acabar; devolve false se o ctx acabou.
func sleep(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
