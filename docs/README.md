# Silo server docs

**Running Silo?** Guides for installing, configuring, and troubleshooting a
server are in the [user manual](https://siloserver.org/docs). Its source is
[Silo-Server/siloserver.org](https://github.com/Silo-Server/siloserver.org);
send documentation fixes there.

This folder is for people and coding agents changing the server:

- [`architecture/`](architecture/) records how each subsystem works, its
  invariants, and the contracts it keeps.
  [api-contract.md](architecture/api-contract.md) and
  [v1-scope.md](architecture/v1-scope.md) are the usual starting points.
- The `*-api.md` files are API references for client and integration
  developers. [api-docs.md](api-docs.md) covers the interactive OpenAPI viewer
  every server serves.
- [`design/`](design/) holds design notes and schemas for client-facing
  protocols.
- [non-goals.md](non-goals.md), [ai-contributions.md](ai-contributions.md), and
  [release-versioning.md](release-versioning.md) are project policy.
- [update-to-1.0.md](update-to-1.0.md) is the draft procedure for updating an
  alpha server to 1.0. It moves to the manual when 1.0 ships.

[DEVELOPMENT.md](../DEVELOPMENT.md) covers building and testing, and
[CONTRIBUTING.md](../CONTRIBUTING.md) covers pull requests.
