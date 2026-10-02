/**
 * Whether outbound mail can actually send, mirrored from the server's own
 * validation (`internal/config/admin_settings.go`): the switch on, a host, AND
 * a sender address — enabling email with no from-address is rejected there,
 * but legacy rows and single-key writes can still leave that state stored.
 * One rule for every surface (settings page, overview tile) so they cannot
 * drift into disagreeing about readiness.
 */
export function emailReady(enabled: boolean, smtpHost: string, fromAddress: string): boolean {
  return enabled && smtpHost.trim() !== "" && fromAddress.trim() !== "";
}
