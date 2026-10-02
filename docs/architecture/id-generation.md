# Sonyflake ID generation

`internal/idgen` mints the locally generated IDs Silo stores: rows such as
people, collections, sections, downloads and admin jobs, and the `content_id` of
items that have no provider anchor. An ID is a 63-bit Sonyflake: 39 bits of time
in 10 ms units since 2024-01-01 UTC, 8 bits of sequence, and 16 bits of machine
ID. Two processes can mint the same ID only if they share a machine ID.

## Machine ID leases

Every process that mints IDs leases its machine ID from PostgreSQL, so replicas
sharing a database never share one, whatever their network addresses.

- `idgen.Start` runs once at startup on API and integrated nodes, after
  migrations and before anything mints an ID. Proxy and transcode nodes mint no
  IDs and do not lease. A claim takes a row in `idgen_machine_leases` under a
  transaction-scoped advisory lock, so concurrent starts cannot claim the same
  row.
- A claim takes a random machine ID that has never been leased. Only when all
  65,536 have been used does it reuse one, and then only the lease that expired
  longest ago and at least an hour ago. The hour keeps clock skew between the
  old and new holder from placing their IDs in the same time slot.
- The lease lasts two minutes and renews every 20 seconds. The process tracks
  its own deadline on the monotonic clock, measured from before each renewal
  request, so the local deadline always falls before the database expiry. It
  also checks the same deadline on the wall clock, because on Linux the
  monotonic clock stops while the host is suspended. Either clock passing the
  deadline ends the lease, so a wall-clock step can only shorten it.
- `NextID` returns `ErrLeaseExpired` once the local deadline passes without a
  renewal, and works again after a later renewal succeeds. If another process
  has taken the machine ID in the meantime, the renewal claims a new one.
- `Lease.Stop` ends renewal but leaves the current lease usable until it
  expires, so shutdown can drain in-flight writers.

A process that mints IDs without calling `Start` gets `ErrNotStarted`. Test
binaries are the exception: they use a random machine ID, because test databases
are disposable.

## Where a duplicate would land

Most IDs are primary keys, so a duplicate fails the write. Two uses would not
fail:

- `downloads.batch_id` groups the downloads of one series or season request and
  has no unique constraint. A duplicate merges two batches for the same user and
  device.
- `media_items` is written with `ON CONFLICT (content_id) DO UPDATE`, so a
  duplicate `content_id` would overwrite an unrelated item.

Both depend on the lease invariant above rather than on a database constraint.
