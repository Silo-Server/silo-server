# Authentication-provider managed roles

An `auth_provider.v1` plugin may opt into managing the linked Silo account's coarse role after successful authentication.

## Response contract

The provider returns both claims:

```json
{
  "silo_role_managed": true,
  "silo_role": "admin"
}
```

The supported role values are `user` and `admin`.

The host ignores `silo_role` unless `silo_role_managed` is present and is the boolean `true`. If the managed marker is true, a missing, empty, non-string, or unsupported role fails authentication rather than partially applying authorization state.

Providers that omit the managed marker retain existing behavior: new auto-provisioned accounts default to `user`, and existing linked accounts keep their locally assigned roles.

## Synchronization behavior

The managed role is evaluated when an account is provisioned and on every successful linked login, including OAuth completion.

When the role changes:

- promotion to `admin` clears the normal-user access group so its ceilings do not cap an administrator;
- demotion to `user` restores default user permissions and assigns the current default access group;
- the transition is written to structured logs with the plugin installation, capability, user, previous role, and new role.

Demotion does not reconstruct custom permissions or access-group assignments that existed before promotion. A provider-managed account returns to the server's current default user policy. This is deliberate and should be considered before enabling external role management.

## Trust model

An installed authentication plugin executes trusted server-side code and receives credentials for its own provider. Returning `silo_role_managed=true` additionally allows that plugin to request promotion or demotion of accounts linked to its installation.

The contract is intentionally narrow. A plugin cannot return arbitrary role names, permissions, library IDs, or access-group IDs. Administrators should install authentication providers only from trusted publishers and keep a working local administrator account for recovery.

## Compatibility and future SDK work

The claims use the existing protobuf `Struct`, so the v1 wire schema remains additive. A future plugin SDK may define typed fields or constants for this contract. Any replacement should preserve compatibility with providers already returning these documented claims.
