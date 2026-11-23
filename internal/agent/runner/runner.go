// SPDX-License-Identifier: AGPL-3.0-only

// Package runner junta coleta, fila e envio: é o que os comandos enroll e
// run do invenire-agent executam.
package runner

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"time"

	"github.com/invenire-hq/invenire/internal/agent/collect"
	"github.com/invenire-hq/invenire/internal/agent/queue"
	"github.com/invenire-hq/invenire/internal/agent/state"
	"github.com/invenire-hq/invenire/internal/agent/transport"
	"github.com/invenire-hq/invenire/internal/wire"
	"github.com/invenire-hq/invenire/pkg/snapshot"
)

// ErrRevoked indica que o servidor não reconhece mais o certificado do agente.
var ErrRevoked = errors.New("o servidor recusou o certificado do agente (revogado ou desconhecido): é preciso cadastrar de novo")

// EnrollOptions são os parâmetros do cadastro.
type EnrollOptions struct {
	ServerURL string
	Token     string
	// CAPin é o SHA-256 da CA que assina o certificado do servidor. Nulo =
	// servidor com certificado público, conferido pelas CAs do sistema.
	CAPin        []byte
	SystemRoots  *x509.CertPool // só para testes; nulo usa as do sistema
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

	tlsCfg, err := serverTLS(opts.ServerURL, opts.CAPin, opts.SystemRoots)
	if err != nil {
		return state.Config{}, err
	}
	client, err := transport.NewClient(opts.ServerURL, tlsCfg)
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
	// Com pin, a CA que vem na resposta é a que o agente vai usar daqui em
	// diante: precisa ser a mesma do pin. Sem pin, o servidor é conferido
	// pelas CAs do sistema, e uma CA na resposta é ignorada.
	var caPEM []byte
	if opts.CAPin != nil {
		if b, _ := pem.Decode([]byte(resp.CA)); b == nil || !transport.PinMatches(b.Bytes, opts.CAPin) {
			return state.Config{}, errors.New("a CA devolvida no cadastro não confere com o pin")
		}
		caPEM = []byte(resp.CA)
	}

	cfg := state.Config{ServerURL: opts.ServerURL, AgentID: resp.AgentID.String(), AssetID: resp.AssetID.String()}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := dir.SaveEnrollment(cfg, keyPEM, []byte(resp.Certificate), caPEM); err != nil {
		return state.Config{}, err
	}
	// Cadastro novo começa um encadeamento novo.
	return cfg, dir.SetLastSum("")
}

// serverTLS confere o servidor pelo pin ou, sem pin, pelas CAs do sistema.
func serverTLS(serverURL string, pin []byte, roots *x509.CertPool) (*tls.Config, error) {
	if pin == nil {
		return transport.SystemTLS(roots), nil
	}
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("URL do servidor: %w", err)
	}
	return transport.PinnedTLS(pin, u.Hostname()), nil
}

// SetServerOptions são os parâmetros de SetServer.
type SetServerOptions struct {
	ServerURL   string
	CAPin       []byte         // como em EnrollOptions
	SystemRoots *x509.CertPool // só para testes
}

// SetServer aponta o agente para outro servidor sem recadastrar: o crachá
// continua o mesmo. Só grava depois de o servidor novo reconhecer o crachá,
// para um endereço errado não deixar o agente sem servidor.
func SetServer(ctx context.Context, dir state.Dir, opts SetServerOptions) (wire.AgentInfo, error) {
	cfg, err := dir.Config()
	if err != nil {
		return wire.AgentInfo{}, err
	}
	tlsCfg, err := serverTLS(opts.ServerURL, opts.CAPin, opts.SystemRoots)
	if err != nil {
		return wire.AgentInfo{}, err
	}
	current, err := dir.TLSConfig()
	if err != nil {
		return wire.AgentInfo{}, err
	}
	tlsCfg.Certificates = current.Certificates
	client, err := transport.NewClient(opts.ServerURL, tlsCfg)
	if err != nil {
		return wire.AgentInfo{}, err
	}
	info, chain, err := client.Agent(ctx)
	var he *transport.HTTPError
	if errors.As(err, &he) && he.Revoked() {
		return info, errors.New("o servidor novo não reconhece este agente: o tenant dele foi importado lá?")
	}
	if err != nil {
		return info, err
	}
	if info.AgentID.String() != cfg.AgentID {
		return info, fmt.Errorf("o servidor novo diz que este crachá é do agente %s, não de %s", info.AgentID, cfg.AgentID)
	}
	var caPEM []byte
	if opts.CAPin != nil {
		for _, c := range chain {
			if c.IsCA && transport.PinMatches(c.Raw, opts.CAPin) {
				caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
			}
		}
		if caPEM == nil {
			return info, errors.New("o servidor novo não apresentou a CA do pin")
		}
	}
	return info, dir.SetServer(opts.ServerURL, caPEM)
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
	// SystemRoots substitui as CAs do sistema quando o servidor é conferido
	// por elas (ServerTrust "system"). Só para testes.
	SystemRoots *x509.CertPool
	// RenewBefore é quanto antes do vencimento o crachá é renovado. Zero = um
	// terço da validade (oito meses, num crachá de dois anos).
	RenewBefore time.Duration

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
	if tlsCfg.RootCAs == nil {
		tlsCfg.RootCAs = a.SystemRoots
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

// RenewIfDue pede um crachá novo quando o atual está perto de vencer, e passa
// a usá-lo. A chave continua a mesma. Falhar não é grave: o crachá atual
// segue valendo e a próxima coleta tenta de novo.
func (a *Agent) RenewIfDue(ctx context.Context) (renewed bool, err error) {
	cert, key, err := a.Dir.Identity()
	if err != nil {
		return false, err
	}
	before := a.RenewBefore
	if before <= 0 {
		before = cert.NotAfter.Sub(cert.NotBefore) / 3
	}
	if time.Until(cert.NotAfter) > before {
		return false, nil
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return false, err
	}
	resp, err := a.client.Renew(ctx, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})))
	if err != nil {
		return false, fmt.Errorf("renovar o crachá (vence em %s): %w", cert.NotAfter.Format(time.DateOnly), err)
	}
	// Só troca por um crachá que sirva: da nossa chave e que dure mais.
	b, _ := pem.Decode([]byte(resp.Certificate))
	if b == nil {
		return false, errors.New("renovar o crachá: resposta sem certificado")
	}
	fresh, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return false, fmt.Errorf("renovar o crachá: %w", err)
	}
	pub, ok := key.Public().(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !pub.Equal(fresh.PublicKey) || !fresh.NotAfter.After(cert.NotAfter) {
		return false, errors.New("renovar o crachá: o servidor devolveu um certificado de outra chave ou que não vence depois do atual")
	}
	if err := a.Dir.SaveCert([]byte(resp.Certificate)); err != nil {
		return false, err
	}
	return true, a.Open()
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
// Devolve ErrRevoked só quando o servidor recusa o crachá em si
// (HTTPError.Revoked); outro 401, e o 413 de proxy, são tratados como
// momentâneos: o snapshot fica na fila.
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
		case errors.As(err, &he) && he.Revoked():
			return sent, ErrRevoked
		case errors.As(err, &he) && he.Status == http.StatusUnauthorized:
			// O crachá não foi recusado; o mais provável é um proxy no
			// caminho. O snapshot fica na fila e o envio é tentado de novo.
			return sent, fmt.Errorf("%w (não é revogação: confira o proxy no caminho, se houver)", err)
		case errors.As(err, &he) && he.Status == http.StatusRequestEntityTooLarge && !he.Permanent():
			return sent, fmt.Errorf("%w (limite de corpo de um proxy no caminho? o servidor aceita até 32 MiB)", err)
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
		if renewed, err := a.RenewIfDue(ctx); err != nil {
			a.Log.WarnContext(ctx, "crachá não renovado; o atual continua em uso", "err", err)
		} else if renewed {
			a.Log.InfoContext(ctx, "crachá renovado")
		}
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
