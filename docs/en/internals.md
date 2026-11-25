[Português](../funcionamento.md) | English

# How Invenire works inside

There are three programs: **PostgreSQL**; the **server** (`invenire-server`), which receives,
organizes and shows the inventories (API at `/v1` and web dashboard on the same port); and the
**agent** (`invenire-agent`), installed on each machine.

## Organizations, units and labels

The **organization** (tenant) is the hard boundary: another organization, another contract,
another CA. A server can have several; each dashboard user sees only their own. Inside it,
organizing the machines is a separate layer, attached to each asset:

- **Unit**: a free-form tree, with whatever depth and level names you like
  (Sede → Financeiro → Recepção; Filial Centro → Andar 2). Each asset is in at most one unit. On
  the command line, a unit is given by its path, with `/` between levels, which is why a name
  cannot contain `/`.
- **Label**: what is not hierarchy, several per asset: `ambiente=produção`, `dados=pessoais`. The
  key has lowercase letters, digits, `_`, `.` and `-`; the value, up to 256 characters.

A machine enrolled with a unit's token lands in that unit. Re-enrolling a machine that already
has a unit does not move it: a person chose its unit. The database guarantees, by a foreign key
including `tenant_id`, that asset, token and unit always belong to the same organization.

## Ingestion

The server runs ingestion in the same process. It wakes up on every new snapshot and, with no
notification, looks for pending ones every 30 seconds, including those that arrived before the
server started. Each snapshot is applied in a transaction, in collection order:

1. Each software entry becomes a raw name in `software_raw`. Those still without a product go
   through the normalization rules (below). Linux packages carry the PURL in the version
   (`pkg:deb/ubuntu/curl@8.5.0-2ubuntu10.4`).
2. The asset's hostname, OS, manufacturer and model are updated, with `asset_changed` in
   `change_log` when they change.
3. Identified software is compared with the asset's open installations, by the key
   version + path. New ones open (`software_installed`), present ones get `last_snapshot_id` and
   missing ones close with `removed_at` (`software_removed`). A reinstall opens a new row, and the
   old one stays in the history.

Cases that do not touch the current state:

- **Software without a product** does not become an installation. It stays in the list of
  unidentified software until it is mapped.
- **Software collector with an error** (`collector_errors`): the list may be incomplete, so
  nothing is closed.
- **Out-of-order snapshot** (collected before the last one already applied): it only feeds
  `software_raw`, and `change_log` records `snapshot_out_of_order`.
- **Unreadable payload**: it is kept with the reason in `snapshot.ingest_error` and does not block
  the queue.

Text coming from the machine is stored without control or invisible formatting characters and
truncated ([security.md](security.md#text-coming-from-the-machines)).

## Normalization

`internal/server/normalize` recognizes the product of each raw name, in this order:

| Rule | Example | Confidence |
|---|---|---|
| Catalog (`catalog.json`) | "Google Chrome" (registry) and `google-chrome-stable` (dpkg) → Google Chrome | 1 |
| Linux package | `curl` (dpkg or rpm) → curl | 1 |
| Windows heuristic | "Cisco Packet Tracer 8.2.2 64Bit" from "Cisco Systems, Inc." → Cisco Systems / Packet Tracer | 0.7 with vendor, 0.5 without |

Only what reaches `--auto-map-confidence` (default 0.9) is mapped without review; below that, the
result shows as a suggestion in the list of unidentified software (in the dashboard, **Software**
> unidentified).

**Shared catalog and organization products.** What the rules produce is the shared catalog, the
same for every organization (`vendor` and `software` with a null `tenant_id`;
`software_raw.software_id` holds only rule mappings). Manual mappings live in
`tenant_software_map`: they apply only to the assets of the mapping user's organization and win
over the rules, which never change them. If the requested product or vendor is already in the
shared catalog, it is reused (`product_scope: "catalog"`); if not, it is created for that
organization only (`product_scope: "tenant"`), so the name of an internal system does not show up
for others. Undoing a manual mapping brings back the rule, if there is one, or returns the name to
the list of unidentified software.

When a name is mapped, by a rule or by hand, the assets that have it (`asset_software_raw`; for
manual mappings, only the organization's) get `remap_pending`, and ingestion recalculates their
installations from the last snapshot, without waiting for the next one. At startup, the server
runs the rules again over every name they can change: a new catalog applies to what has already
been ingested, and a removed rule undoes what it had mapped.

To add a product to the catalog, see [development.md](development.md#software-catalog).

## Agents and revocation

Each asset has one agent: enrolling the same machine again revokes the previous agent, and the
`agent_enrolled` entry in `change_log` says how many were revoked (`revoked_agents`). Revoking an
agent means filling in `agent.revoked_at` (in the dashboard, **Máquinas** (Machines) > revoke); its
next request gets `401`, and it stops.

## Export

The whole organization goes out in a `.tar.gz` package with the exact bytes the agents sent, and
it can be verified without a database. Format and usage in [export.md](export.md).
