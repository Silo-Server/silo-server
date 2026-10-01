import { ExternalLink, Loader2 } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import {
  useAdminNetworkAccessStatus,
  useConnectNetworkAccess,
  useDisconnectNetworkAccess,
  useNetworkAccessCapabilities,
  type NetworkAccessHostState,
  type NetworkAccessHostStatus,
  type NetworkAccessProviderSummary,
} from "@/hooks/queries/admin/networkAccess";
import { formatDateTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";

import { FieldGroup } from "./FieldGroup";

// `state` is the provider's own vocabulary plus the host-side `unavailable`;
// admins get plain wording.
const STATE_LABELS: Record<NetworkAccessHostState, string> = {
  disconnected: "Disconnected",
  awaiting_authorization: "Waiting for authorization",
  connecting: "Connecting",
  connected: "Connected",
  error: "Error",
  unavailable: "Plugin not running",
};

const STATE_DOT: Record<NetworkAccessHostState, string> = {
  disconnected: "bg-muted-foreground/40",
  awaiting_authorization: "bg-amber-500",
  connecting: "bg-amber-500",
  connected: "bg-emerald-500",
  error: "bg-destructive",
  unavailable: "bg-muted-foreground/40",
};

function stateLabel(state: string): string {
  return STATE_LABELS[state as NetworkAccessHostState] ?? state;
}

function StatePill({ state }: { state: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-sm" data-state={state}>
      <span
        aria-hidden="true"
        className={cn(
          "size-2 rounded-full",
          STATE_DOT[state as NetworkAccessHostState] ?? "bg-muted-foreground/40",
        )}
      />
      {stateLabel(state)}
    </span>
  );
}

function hostLabel(host: NetworkAccessHostStatus["host"]): string {
  const role = host.role === "api" ? "API server" : "Proxy node";
  return host.name ? `${host.name} · ${role}` : role;
}

/**
 * One host's row: who it is, the provider's state there, where clients
 * reach it, and the connect/disconnect control. While a host waits for
 * enrollment the auth URL is the one thing the admin needs, so it sits
 * beside the state rather than behind a disclosure.
 */
function HostRow({ provider, host }: { provider: string; host: NetworkAccessHostStatus }) {
  const connect = useConnectNetworkAccess();
  const disconnect = useDisconnectNetworkAccess();
  const busy = connect.isPending || disconnect.isPending;
  const unavailable = host.state === "unavailable";
  const connected = host.state === "connected";
  const pending = host.state === "connecting" || host.state === "awaiting_authorization";
  const showDisconnect = connected || pending;
  return (
    <li
      className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3 py-4 first:pt-0 last:pb-0"
      data-testid={`network-access-host-${provider}-${host.host.id}`}
      data-state={host.state}
    >
      <div className="min-w-0 flex-1 space-y-1.5">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <span className="font-medium">{hostLabel(host.host)}</span>
          <StatePill state={host.state} />
          {host.provider_version ? (
            <Badge variant="outline" className="font-normal">
              {host.provider_version}
            </Badge>
          ) : null}
        </div>
        {host.origin ? (
          <p className="text-muted-foreground text-sm">
            Clients reach this host at{" "}
            <a
              href={host.origin}
              target="_blank"
              rel="noreferrer"
              className="text-foreground underline underline-offset-4"
            >
              {host.origin}
            </a>
            {host.hostname && host.hostname !== host.origin ? ` (${host.hostname})` : ""}
          </p>
        ) : host.hostname ? (
          <p className="text-muted-foreground text-sm">{host.hostname}</p>
        ) : null}
        {host.addresses.length > 0 ? (
          <p className="text-muted-foreground text-xs">{host.addresses.join(", ")}</p>
        ) : null}
        {host.state === "awaiting_authorization" && host.auth_url ? (
          <p className="text-sm">
            <a
              href={host.auth_url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 font-medium underline underline-offset-4"
            >
              Open the authorization page
              <ExternalLink className="size-3.5" aria-hidden="true" />
            </a>
            <span className="text-muted-foreground"> to finish enrolling this host.</span>
          </p>
        ) : null}
        {host.error ? (
          <p className="text-destructive text-sm" role="status">
            {host.error}
          </p>
        ) : null}
        {host.updated_at ? (
          <p className="text-muted-foreground text-xs">
            Last heard from {formatDateTime(host.updated_at, { seconds: false })}
          </p>
        ) : null}
      </div>
      <div className="flex shrink-0 items-center gap-2">
        {showDisconnect ? (
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={busy}
            onClick={() => disconnect.mutate({ provider, hosts: [host.host.id] })}
          >
            {busy ? <Loader2 className="size-4 animate-spin" aria-hidden="true" /> : null}
            Disconnect
          </Button>
        ) : (
          <Button
            type="button"
            size="sm"
            disabled={busy || unavailable}
            title={unavailable ? "Start the plugin from the Plugins page first." : undefined}
            onClick={() => connect.mutate({ provider, hosts: [host.host.id] })}
          >
            {busy ? <Loader2 className="size-4 animate-spin" aria-hidden="true" /> : null}
            Connect
          </Button>
        )}
      </div>
    </li>
  );
}

function ProviderGroup({ provider }: { provider: NetworkAccessProviderSummary }) {
  const status = useAdminNetworkAccessStatus(provider.provider);
  const hosts = status.data?.hosts ?? [];

  return (
    <FieldGroup
      label={provider.display_name}
      description={`Access Silo through your ${provider.display_name} network without port forwarding.`}
    >
      {status.isLoading ? (
        <div className="space-y-3" aria-busy="true">
          <Skeleton className="h-5 w-2/3" />
          <Skeleton className="h-4 w-1/2" />
        </div>
      ) : status.isError ? (
        <p className="text-destructive text-sm" role="alert">
          {status.error instanceof Error ? status.error.message : "Could not read provider status."}
        </p>
      ) : hosts.length === 0 ? (
        <p className="text-muted-foreground text-sm">No host runs this provider yet.</p>
      ) : (
        <ul className="divide-border divide-y">
          {hosts.map((host) => (
            <HostRow key={host.host.id} provider={provider.provider} host={host} />
          ))}
        </ul>
      )}
    </FieldGroup>
  );
}

/**
 * Settings → Network Access: every installed overlay-network provider
 * (Tailscale and the like) with its state on each host that runs it. The
 * providers themselves are plugins; installing one is done on the Plugins
 * page, so this page only reads them and drives connect/disconnect.
 */
export default function NetworkAccessSettings() {
  const capabilities = useNetworkAccessCapabilities();
  const providers = capabilities.data?.providers ?? [];

  return (
    <div className="space-y-6">
      <SettingsPageHeader title="Network Access" />
      <p className="text-muted-foreground max-w-prose text-sm">
        Network access providers give this server an identity on an overlay network such as
        Tailscale, so clients can reach it without port forwarding or a public reverse proxy.
        Providers are plugins: install one from the Plugins page and it appears here.
      </p>
      {capabilities.isLoading ? (
        <div className="space-y-3" aria-busy="true">
          <Skeleton className="h-6 w-1/3" />
          <Skeleton className="h-20 w-full" />
        </div>
      ) : capabilities.isError ? (
        <p className="text-destructive text-sm" role="alert">
          {capabilities.error instanceof Error
            ? capabilities.error.message
            : "Could not read network access providers."}
        </p>
      ) : providers.length === 0 ? (
        <FieldGroup label="No providers installed">
          <p className="text-muted-foreground text-sm">
            No installed plugin provides network access. Install a network access provider from the
            Plugins page to connect this server to an overlay network.
          </p>
        </FieldGroup>
      ) : (
        providers.map((provider) => <ProviderGroup key={provider.provider} provider={provider} />)
      )}
    </div>
  );
}
