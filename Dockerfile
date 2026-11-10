# Imagem do invenire-server. Um binário estático sobre distroless, sem shell,
# rodando como usuário sem privilégio.
#
#   docker build --build-arg VERSION=0.1.0 -t invenire-server:0.1.0 .
#
# Os dados (CA da instalação, chave-mestra) ficam em /var/lib/invenire/data:
# monte um volume em /var/lib/invenire e inclua-o no backup.

FROM golang:1.26.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/invenire-server ./cmd/invenire-server \
 && mkdir -p /out/var/lib/invenire

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/invenire-server /usr/local/bin/invenire-server
COPY --from=build --chown=65532:65532 /out/var/lib/invenire /var/lib/invenire
WORKDIR /var/lib/invenire
VOLUME /var/lib/invenire
EXPOSE 8443
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=6s --start-period=20s CMD ["invenire-server", "health"]
ENTRYPOINT ["invenire-server"]
CMD ["serve", "--listen", ":8443", "--data", "data"]
