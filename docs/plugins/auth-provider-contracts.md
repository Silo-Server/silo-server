# Authentication-provider host contracts

This document describes the host-side conventions used by `auth_provider.v1` plugins for configuration checks and optional role synchronization. These conventions are additive: providers that do not advertise or return them keep the existing authentication behavior.

## Configuration connection check

An authentication capability opts into configuration checks through capability metadata:

```json
{
  "type": "auth_provider.v1",
  "id": "ldap",
  "metadata": {
    "connection_test": true
  }
}
```

When an administrator tests staged configuration, Silo starts a temporary plugin instance with the staged values and preserved saved secrets, then sends an `AuthenticateRequest` containing:

```json
{
  "metadata": {
    "connection_test": true
  }
}
```

The request intentionally contains no username or password. The provider must validate only the configured service connection and return success or a gRPC error. The temporary instance is stopped after the check.

Until the plugin SDK exposes a dedicated connection-check RPC, the metadata key above is the compatibility contract. Providers must not interpret a normal password request as a connection check unless the value is the boolean `true`.

## Provider-managed roles

A provider may opt into managing the linked Silo account's coarse role by returning both claims below after successful authentication:

```json
{
  "silo_role_managed": true,
  "silo_role": "admin"
}
```

The supported role values are `user` and `admin`.

The host ignores `silo_role` unless `silo_role_managed` is present and is the boolean `true`. A malformed managed-role response fails authentication rather than partially applying authorization state.

When the managed role changes:

- promotion to `admin` clears the normal-user access group so its ceilings do not cap an administrator;
- demotion to `user` restores default user permissions and assigns the current default access group;
- the transition is applied on successful login and written to structured logs with the installation, capability, user, previous role, and new role;
- providers that omit the managed marker cannot change an existing role and new accounts retain the normal `user` default.

## Trust model

An installed authentication plugin executes trusted server-side code and receives submitted credentials for its own provider. Enabling provider-managed roles additionally allows that plugin to request promotion or demotion of accounts linked to that installation.

Administrators should install authentication providers only from trusted publishers, keep a working local administrator account for recovery, and review provider configuration before enabling directory-managed roles.

The role contract is deliberately narrow. Plugins cannot return arbitrary role names, permissions, library IDs, or access-group IDs.

## Compatibility and future SDK work

These conventions use existing protobuf `Struct` metadata and claims so they do not change the v1 wire schema. A future plugin SDK may replace either convention with typed fields or dedicated RPCs. Any replacement must preserve backward compatibility for providers using these documented keys.
