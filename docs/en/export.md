[Português](../exportacao.md) | English

# Tenant export format (version 1)

The export package carries an entire tenant from one Invenire server to another: from the cloud
to a self-hosted installation, back again, or to an archive file. It is Invenire's `git clone`.
The format is a public contract: new fields may appear in a compatible version; renaming a field
or changing its meaning requires a new `format_version`. The Go structs that define it are in
[`internal/server/export/format.go`](../../internal/server/export/format.go).

```sh
invenire-server tenant export --tenant "Minha Organização" --out minha-organizacao.invenire.tar.gz [--with-ca-key]
invenire-server tenant verify minha-organizacao.invenire.tar.gz [--export-key <key>]
curl -H "Authorization: Bearer inva_…" -OJ "https://invenire.exemplo.org/v1/export?with_ca_key=true"   # admin scope
```

In the dashboard, the same export is in **Exportar** (Export) (administrator). In the Docker
installation, `tenant export` runs inside the container and writes the package there; the API or
the dashboard deliver the file directly.

## What goes and what does not

What goes is what cannot be recalculated. Left out is what is derived from the snapshots and what
is a secret of the installation.

| Goes | Does not go |
|---|---|
| snapshots (the exact bytes the agent sent) and their metadata | installations (`installation`): recalculated from the snapshots |
| assets, agents, units, labels | raw names and rule mappings: recalculated by the importer's rules |
| the tenant's manual mappings and the products it created | the shared catalog: each server has its own |
| actors (people, AI, pipelines), proposals | API and enrollment tokens: the importer issues new ones |
| the tenant's entire `change_log` | the installation's master key |
| the tenant's CAs (certificates) and, with `--with-ca-key`, the keys | |

## Layout

A `.tar.gz` with these entries, in this order, without a root directory:

| Entry | Contents |
|---|---|
| `tenant_ca.jsonl` | CAs that signed badges for the tenant's agents |
| `units.jsonl` | units (a parent may come after its child) |
| `assets.jsonl` | assets; `collected` is what the machine reported in the last ingested snapshot |
| `labels.jsonl` | labels |
| `agents.jsonl` | agents, active and revoked, with the badge fingerprint and the CA that issued it |
| `actors.jsonl` | actors, without tokens |
| `snapshots.jsonl` | metadata of each snapshot and the path of its bytes |
| `products.jsonl` | products (and vendors) created by hand by the tenant |
| `mappings.jsonl` | manual mappings: the full raw name and the product |
| `proposals.jsonl` | proposals from AI agents and the decisions |
| `changes.jsonl` | the tenant's `change_log`, in id order |
| `snapshots/<sha256>.json` | the exact bytes of each snapshot; the name is the SHA-256 of the content |
| `snapshots/<sha256>.payload.json` | only for snapshots received before the server kept the bytes: the content as rewritten by the database, with no re-verifiable SHA-256 |
| `manifest.json` | last: what is in the package and how to verify it |

`.jsonl` files have one JSON object per line, terminated by `\n`, in UTF-8. Dates in RFC 3339
with a time zone, ids as UUIDs (the same as on the source server: they do not collide on import),
hashes in lowercase hex and bytes (`key_sealed`) in base64.

## manifest.json

```json
{
  "format": "invenire-tenant-export",
  "format_version": 1,
  "tenant": {"id": "35ff3283-…", "name": "Minha Organização"},
  "exported_at": "2026-11-10T14:00:28Z",
  "server_version": "0.3.0",
  "as_of": 421,
  "files": {"assets.jsonl": {"sha256": "…", "bytes": 1234, "records": 1}, "…": {}},
  "snapshot_bodies": 2,
  "snapshot_payloads": 0,
  "ca_key": {"algorithm": "AES-256-GCM", "aad": "tenant_id||tenant_ca_id"}
}
```

- `as_of`: the tenant's last `change_log.id` when the package was read. Everything was read in a
  single transaction, at that instant.
- `files`: SHA-256, size and line count of each `.jsonl`.
- `ca_key`: only present if the package carries some CA key.

## How to verify

`tenant verify` does this, without a database, and any implementation can redo it:

1. `format` and `format_version` are known.
2. Each `.jsonl` is present, with the SHA-256 and line count from the manifest; no files other
   than those listed and `snapshots/`.
3. The SHA-256 of each `snapshots/<sha256>.json` matches its name, and each one corresponds to a
   line in `snapshots.jsonl` (and vice versa).
4. Per asset, in `collected_at` and `received_at` order, each snapshot's `prev_sha256` is the
   previous one's `sha256`. A divergent link does **not** invalidate the package: the server
   accepts the snapshot and records `snapshot_chain_broken`, and the package preserves the
   divergence as it was.
5. References: parent unit, the asset's and the label's unit; the agent's asset and CA; the
   snapshot's asset and agent; the tenant product of each manual mapping.
6. Each CA certificate is valid and has the declared `fingerprint`; with the export key, each key
   opens and is the certificate's key.

The SHA-256 of the whole file, shown by `export` and `verify`, is the package's fingerprint:
write it down to prove later that it is the same file.

## The CA key

Agent badges are signed by the tenant's CA. Taking its key along, the new server keeps issuing
and renewing badges, and agents only change address (`invenire-agent set-server`). Without the
key, the new server checks the current badges, but agents need to be re-enrolled before their
badge expires.

With `--with-ca-key` (or `?with_ca_key=true`), the private key of each non-revoked CA goes out
encrypted in `key_sealed`:

- AES-256-GCM, with a 32-byte **export key** generated on the spot;
- a 12-byte nonce at the start of the ciphertext;
- additional data: the 16 bytes of the tenant id followed by the 16 bytes of the CA id.

The export key does not go in the package. `tenant export` shows it once; the API returns it in
the `Invenire-Export-Key` header. Keep it separate from the package: with both, someone can issue
badges on behalf of the tenant. Check-only CAs (the old installation CA, for agents enrolled
before per-tenant CAs) have no key.

## Audit

Every export is recorded in the tenant's `change_log` as `tenant_exported`, with `as_of`, whether
it carried the CA key and how it went out (`cli` or `api`). The record is written after the
export, so it does not appear in the package itself.

## Import

Not implemented yet. An importer should:

1. verify the package as above;
2. refuse if the tenant (by id) already exists;
3. write the records with the same ids and each snapshot with its original bytes;
4. for each manual mapping, find or create the raw name by source, name and vendor, and link it to
   the product: those with `product_scope` `tenant` come from `products.jsonl`; those with
   `catalog` are looked up by name in the importer's catalog;
5. run every snapshot through ingestion, in collection order, to recalculate the installations.
