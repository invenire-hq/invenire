// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ParseTrustedProxies lê a lista de --trusted-proxies: CIDRs ou IPs soltos,
// separados por vírgula.
func ParseTrustedProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !strings.Contains(f, "/") {
			a, err := netip.ParseAddr(f)
			if err != nil {
				return nil, fmt.Errorf("--trusted-proxies: %q não é IP nem CIDR", f)
			}
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(f)
		if err != nil {
			return nil, fmt.Errorf("--trusted-proxies: %q não é IP nem CIDR", f)
		}
		out = append(out, p.Masked())
	}
	if len(out) == 0 {
		return nil, errors.New("--trusted-proxies vazio: diga de que endereços o proxy reverso conecta (ex.: 127.0.0.1/32)")
	}
	return out, nil
}

// forwardedHeaders são os cabeçalhos com que um proxy conta quem é o cliente.
// Forwarded (RFC 7239) não é lido, mas também não pode vir de fora.
var forwardedHeaders = []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "Forwarded"}

// ProxyHeaders é a porta de entrada do modo --behind-proxy.
//
// Só de uma origem em trusted os cabeçalhos X-Forwarded-* são lidos:
// X-Forwarded-For vira o RemoteAddr (o que aparece nos logs) e um
// X-Forwarded-Proto diferente de https é recusado, porque o token de
// cadastro teria passado em claro entre o agente e o proxy.
//
// De qualquer outra origem, requisição com esses cabeçalhos é recusada (403):
// alguém tentando se passar pelo proxy, ou o servidor exposto direto. Sem
// eles, passa (o healthcheck local, por exemplo). A autenticação do agente
// não depende de nada disto: é a assinatura.
func ProxyHeaders(trusted []netip.Prefix, next http.Handler) http.Handler {
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, ok := remoteAddr(r)
		if !ok || !isTrusted(peer) {
			for _, h := range forwardedHeaders {
				if _, has := r.Header[http.CanonicalHeaderKey(h)]; has {
					writeError(w, http.StatusForbidden, h+" vindo de "+r.RemoteAddr+", fora de --trusted-proxies")
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		if proto := strings.TrimSpace(lastField(r.Header.Values("X-Forwarded-Proto"))); proto != "" &&
			!strings.EqualFold(proto, "https") && r.URL.Path != "/healthz" {
			writeError(w, http.StatusForbidden, "o proxy recebeu esta requisição por "+proto+"; use HTTPS")
			return
		}
		if client, ok := forwardedClient(r.Header.Values("X-Forwarded-For"), isTrusted); ok {
			r.RemoteAddr = net.JoinHostPort(client.String(), "0")
		}
		next.ServeHTTP(w, r)
	})
}

func remoteAddr(r *http.Request) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap(), true
}

// forwardedClient é o cliente pelo X-Forwarded-For: o endereço mais à
// direita que não é um proxy confiável. Os da esquerda foram escritos pelo
// próprio cliente e não valem nada.
func forwardedClient(values []string, isTrusted func(netip.Addr) bool) (netip.Addr, bool) {
	var hops []string
	for _, v := range values {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return netip.Addr{}, false
		}
		a = a.Unmap()
		if !isTrusted(a) || i == 0 {
			return a, true
		}
	}
	return netip.Addr{}, false
}

func lastField(values []string) string {
	if len(values) == 0 {
		return ""
	}
	f := strings.Split(values[len(values)-1], ",")
	return f[len(f)-1]
}
