[Português](SECURITY.md) | English

# Security

## How to report a vulnerability

**Do not open a public issue** for a security vulnerability: it is visible to everyone before
there is a fix.

Use GitHub's private reporting: on the repository page, the **Security** tab > **Report a
vulnerability** (or directly at
<https://github.com/invenire-hq/invenire/security/advisories/new>). Only the maintainers see the
report, and the conversation about the fix happens there.

It helps a lot to include:

- server and agent version (`invenire-server version`, `invenire-agent version`);
- installation scenario (local network, VPS with Caddy, behind a proxy; which proxy);
- what the vulnerability allows, and by whom (someone on the network, an enrolled agent, a
  dashboard `read` user, someone without an account);
- steps to reproduce or a proof of concept.

Do not include real inventory data, or tokens or keys that are in use: replace them with
examples.

## What to expect

- Acknowledgement of receipt within 7 days.
- Assessment and fix plan agreed in the report itself.
- Fix published in a new version, with a security advisory (GitHub Security Advisory) and a line
  in the [CHANGELOG.md](CHANGELOG.md). The reporter gets credit, if they want it.

Invenire is maintained by few people: the timelines above are in good faith, not a service-level
agreement.

## Supported versions

Only the latest version receives security fixes. Update the server
([docs/en/operations.md](docs/en/operations.md#updating-the-server)) and the agents
([docs/en/agents.md](docs/en/agents.md#updating)).

## How Invenire protects itself

The trust model (installation CA and each organization's CA, agent badge, request signing behind a
proxy, first-time setup code, dashboard sessions, stored data) is in
[docs/en/security.md](docs/en/security.md). Behavior described there as expected is not a
vulnerability; if you think the decision is wrong, a regular issue is fine.
