import { useNetworkAccessCapabilities } from "@/hooks/queries/admin/networkAccess";
import type { AdminSession } from "@/api/types";
import { getSessionRouteNodes, type ActivityRouteNode } from "@/pages/adminActivityPresentation";

const BADGE_COLORS: Record<ActivityRouteNode["kind"], string> = {
  transcode: "border-warning/25 bg-warning/10 text-warning",
  proxy: "border-info/25 bg-info/10 text-info",
  server: "border-primary/10 bg-primary/5 text-primary",
  legacy: "border-primary/10 bg-primary/5 text-primary",
};

function OverlayNetworkBadge({ provider }: { provider: string }) {
  const capabilities = useNetworkAccessCapabilities();
  const name =
    capabilities.data?.providers.find((entry) => entry.provider === provider)?.display_name ||
    provider;
  return <NetworkBadge name={name} title={`Playback uses the ${name} network.`} />;
}

function NetworkBadge({ name, title }: { name: string; title: string }) {
  return (
    <span
      title={title}
      aria-label={`Network: ${name}`}
      className="border-primary/20 bg-primary/10 text-primary inline-flex max-w-full items-center gap-1 rounded border px-1.5 py-0.5 text-[9px] font-semibold"
    >
      <span className="opacity-70">Network</span>
      <span aria-hidden="true">·</span>
      <span className="truncate">{name}</span>
    </span>
  );
}

/** The selected access network and serving nodes, shared by all activity views. */
export function PlaybackRouteBadges({ session }: { session: AdminSession }) {
  const provider = session.routing_network_provider;
  return (
    <>
      {provider ? (
        <OverlayNetworkBadge provider={provider} />
      ) : (
        <NetworkBadge
          name={provider === "" ? "Default" : "Unknown"}
          title={
            provider === ""
              ? "Playback uses the default network (LAN, public URL, or reverse proxy)."
              : "This session did not report its access network."
          }
        />
      )}
      {getSessionRouteNodes(session).map((node) => {
        const title =
          node.kind === "server"
            ? `API server${session.reporting_node ? `: ${session.reporting_node}` : ""}${session.routing_egress === "api" ? " (serves media)" : ""}`
            : `${node.label}: ${node.name}`;
        return (
          <span
            key={node.key}
            title={title}
            aria-label={title}
            className={`inline-flex max-w-full items-center gap-1 rounded border px-1.5 py-0.5 text-[9px] font-semibold ${BADGE_COLORS[node.kind]}`}
          >
            {node.kind === "transcode" || node.kind === "proxy" ? (
              <>
                <span className="opacity-70">{node.label}</span>
                <span aria-hidden="true">·</span>
              </>
            ) : null}
            <span className="truncate">{node.kind === "server" ? "API server" : node.name}</span>
          </span>
        );
      })}
    </>
  );
}
