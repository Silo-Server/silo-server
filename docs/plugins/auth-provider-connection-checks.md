# Authentication-provider connection checks

An `auth_provider.v1` plugin may opt into staged configuration checks through capability metadata:

```json
{
  "type": "auth_provider.v1",
  "id": "ldap",
  "metadata": {
    "connection_test": true
  }
}
```

When an administrator tests unsaved configuration, Silo starts a temporary plugin instance with the staged values and any preserved saved secrets. It then sends an `AuthenticateRequest` containing:

```json
{
  "metadata": {
    "connection_test": true
  }
}
```

The request contains no username or password. The provider must validate only the configured service connection and return success or a gRPC error. The temporary instance is stopped after the check, and the host applies a 20-second timeout.

Providers must not interpret a normal password request as a connection check unless the metadata value is the boolean `true`. Capabilities that omit the flag, set it to another type, or set it to `false` are not eligible for the Test connection action.

The provider is responsible for sanitizing its RPC error. Detailed service errors should be logged by the plugin; unauthenticated responses should expose only information safe for an administrator-facing configuration result.

## Compatibility and future SDK work

This convention uses the existing protobuf `Struct`, so the v1 wire schema remains additive. A future plugin SDK may define a dedicated connection-check RPC or typed metadata constant. Any replacement should preserve compatibility with providers already advertising and consuming this documented key.
