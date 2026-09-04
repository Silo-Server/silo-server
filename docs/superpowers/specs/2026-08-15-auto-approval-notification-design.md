# Auto-Approval Notification Design

## Problem

Issue [#590](https://github.com/Silo-Server/silo-server/issues/590) reports that an
auto-approved media request immediately sends its requester a personal
`request.approved` notification. The requester initiated the request and policy guarantees
its approval, so this personal notification is redundant.

The request service currently publishes automatic and manual approvals through the same
`LifecycleNotifier.RequestApproved` hook. The notification adapter cannot distinguish their
origins and therefore always sends both the server-channel approval event and the requester's
personal approval delivery.

## Desired Behavior

- Automatic approval remains an approval lifecycle transition.
- Automatic approval continues to be recorded by the request repository.
- Automatic approval continues to emit configured server/admin-channel approval notifications.
- Automatic approval does not create a personal `request.approved` delivery for the requester.
- Manual approval continues to emit both server/admin-channel and requester-facing approval
  notifications.
- Request fulfillment notification behavior remains unchanged.

## Design

Add an explicit approval origin to the request lifecycle notification contract. The request
package will define an `ApprovalOrigin` type with `ApprovalOriginManual` and
`ApprovalOriginAutomatic` constants, and `LifecycleNotifier.RequestApproved` will accept it
alongside the request.

The request service already owns the information needed to select the origin:

- `CreateRequest` passes `ApprovalOriginAutomatic` when effective policy and a compatible
  configured integration cause the newly created request to start approved.
- `Approve` passes `ApprovalOriginManual` after an administrator approves a pending request.

`RequestLifecycleNotifier.RequestApproved` will always post the approval event to configured
server channels. It will dispatch the requester's personal `request.approved` delivery unless
the origin is `ApprovalOriginAutomatic`. No notification destination logic will move into the
request service.

Unknown approval-origin values will use the conservative manual behavior and retain the
requester notification. This makes an omitted or future origin fail toward preserving an
existing user-visible notification rather than silently dropping one.

## Alternatives Considered

### Separate automatic-approval hook

Adding `RequestAutoApproved` would make the two paths explicit, but it would duplicate the
shared approval lifecycle behavior and expand the notifier interface for a distinction that is
naturally event metadata.

### Bypass the lifecycle notifier

The automatic path could skip `RequestApproved` and post directly to server channels. This
would couple the request service to notification destinations and split approval behavior
across packages, so it is rejected.

## Testing

Regression tests will verify the contract at both relevant boundaries:

- The request service marks the approval origin as `ApprovalOriginAutomatic` for policy-driven
  approval and as `ApprovalOriginManual` for administrator approval.
- The notification lifecycle adapter always emits the server-channel approval event, suppresses
  personal delivery for `ApprovalOriginAutomatic`, and retains personal delivery for
  `ApprovalOriginManual`.

The tests will use complete request fixtures and assert the destination-visible event type and
delivery behavior. Existing request and notification tests will be run to ensure submission,
decline, fulfillment, and manual approval behavior remain intact.

## Compatibility and Scope

This is an internal Go interface change. It does not change the `/api/v1` contract, database
schema, persisted lifecycle events, notification delivery types, frontend rendering, or client
behavior. No migration is required.
