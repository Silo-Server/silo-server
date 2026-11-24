# Client diagnostics schemas

The canonical client-diagnostics v1 contract lives in `v1/`.
Clients vendor the schema files, attribute registry, and fixtures from this directory.
Servers accept supported schema versions and ignore unknown additive fields.
Within v1, changes are additive only: new optional fields, enum values after coordinated client support, and new allowlisted archive entries.
Breaking changes require a new versioned contract directory and explicit server support.
