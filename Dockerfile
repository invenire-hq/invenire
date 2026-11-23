# Imagem do invenire-server. Um binário estático sobre distroless, sem shell,
# rodando como usuário sem privilégio. Leva também os executáveis do agente
# (Windows amd64, Linux amd64 e arm64) com o SHA256SUMS, que o servidor
# distribui em /download/ e no pacote do instalador montado pelo painel.
#
#   docker build --build-arg VERSION=0.1.0 -t invenire-server:0.1.0 .
#
# A imagem publicada (ghcr.io/invenire-hq/invenire-server) é montada pelo
# .github/workflows/release.yml para amd64 e arm64. A compilação roda sempre
# na arquitetura de quem monta (BUILDPLATFORM) e gera o binário da imagem de
# destino por compilação cruzada, sem emulação.
#
# Os dados (CA da instalação, chave-mestra) ficam em /var/lib/invenire/data:
# monte um volume em /var/lib/invenire e inclua-o no backup.

FROM --platform=$BUILDPLATFORM golang:1.26.1-alpine AS src
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Os executáveis do agente são os mesmos para qualquer arquitetura da imagem.
# Os mesmos nomes do scripts/build.sh: o servidor e os scripts de instalação
# procuram por eles.
FROM src AS agent
ARG VERSION=dev
RUN set -e; mkdir -p /out/agent; \
    for p in windows/amd64 linux/amd64 linux/arm64; do \
      os=${p%/*}; arch=${p#*/}; ext=""; [ "$os" = windows ] && ext=.exe; \
      CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/agent/invenire-agent-$os-$arch$ext ./cmd/invenire-agent; \
    done; \
    cd /out/agent && sha256sum -- * > SHA256SUMS && cat SHA256SUMS

FROM src AS server
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/invenire-server ./cmd/invenire-server \
 && mkdir -p /out/var/lib/invenire

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="invenire-server" \
      org.opencontainers.image.description="Servidor do Invenire: inventário de máquinas (API, painel web e distribuição do agente)" \
      org.opencontainers.image.source="https://github.com/invenire-hq/invenire" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=server /out/invenire-server /usr/local/bin/invenire-server
COPY --from=agent /out/agent /usr/local/share/invenire/agent
COPY --from=server --chown=65532:65532 /out/var/lib/invenire /var/lib/invenire
WORKDIR /var/lib/invenire
VOLUME /var/lib/invenire
EXPOSE 8443
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=6s --start-period=20s CMD ["invenire-server", "health"]
ENTRYPOINT ["invenire-server"]
CMD ["serve", "--listen", ":8443", "--data", "data"]
