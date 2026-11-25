[Português](../README.md) | English

# Invenire documentation

In English. The original is in Brazilian Portuguese, in [`docs/`](../README.md); this folder
mirrors it with English file names (`instalar/rede-local.md` → `install/local-network.md`, and so
on). The software's own screens and messages are in Portuguese: those texts appear here as they
are, with a translation where it helps.

## Installing the server

- [Which scenario is yours](install/choosing.md)
- [Local network](install/local-network.md): no domain, port 8443, agents with a pin
- [VPS](install/vps.md): domain, Caddy and automatic Let's Encrypt
- [Reverse proxy](install/reverse-proxy.md): Nginx (complete recipe) and the matrix of other proxies

## Using and maintaining

- [Web dashboard](dashboard.md): first-time setup and screens
- [Agents](agents.md): installing in three ways, updating, changing servers, uninstalling, logs
- [Operations](operations.md): backup, restore, updates, monitoring, command line
- [When things go wrong](troubleshooting.md): messages, machine states, proxy and dashboard
- [Pilot plan](../piloto.md): order, success criteria and what to record (in Portuguese only)

## Reference

- [Security](security.md): who trusts whom, what the proxy sees, dashboard and package
- [API](api.md): `/v1` routes and queries
- [How it works inside](internals.md): units, ingestion, normalization
- [Organization export](export.md): package format and future import
- [Development](development.md): layout, build, tests, sqlc
- [Plan: simple installation and documentation](../plano-instalacao-simples.md) (history, in
  Portuguese only)

## Project

- [How to contribute](../../CONTRIBUTING.en.md) and the [contributor agreement](../../CLA.md) (in
  Portuguese)
- [How to report a security issue](../../SECURITY.en.md)
- [Changes in each version](../../CHANGELOG.md) (in Portuguese)
- [License (AGPL-3.0)](../../LICENSE)
