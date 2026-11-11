import { useT } from "@/i18n";
import { useNetworkAccessCapabilities } from "@/hooks/queries/admin/networkAccess";
import type { AdminSession } from "@/api/types";
import { getSessionRouteNodes, type ActivityRouteNode } from "@/pages/adminActivityPresentation";

/** Decorative separator between values; not text, so not translated. */
const SEPARATOR = "·";

const BADGE_COLORS: Record<ActivityRouteNode["kind"], string> = {
  transcode: "border-warning/25 bg-warning/10 text-warning",
  proxy: "border-info/25 bg-info/10 text-info",
  server: "border-primary/10 bg-primary/5 text-primary",
  legacy: "border-primary/10 bg-primary/5 text-primary",
};

function OverlayNetworkBadge({ provider }: { provider: string }) {
  const t = useT();
  const capabilities = useNetworkAccessCapabilities();
  const name =
    capabilities.data?.providers.find((entry) => entry.provider === provider)?.display_name ||
    provider;
  return <NetworkBadge name={name} title={t("web.player.routeBadge.networkTitle", { name })} />;
}

function NetworkBadge({ name, title }: { name: string; title: string }) {
  const t = useT();
  return (
    <span
      title={title}
      aria-label={t("web.player.routeBadge.networkAria", { name })}
      className="border-primary/20 bg-primary/10 text-primary inline-flex max-w-full items-center gap-1 rounded border px-1.5 py-0.5 text-[9px] font-semibold"
    >
      <span className="opacity-70">{t("web.player.routeBadge.network")}</span>
      <span aria-hidden="true">{SEPARATOR}</span>
      <span className="truncate">{name}</span>
    </span>
  );
}

/** The selected access network and serving nodes, shared by all activity views. */
export function PlaybackRouteBadges({ session }: { session: AdminSession }) {
  const t = useT();
  const provider = session.routing_network_provider;
  return (
    <>
      {session.stream_location && (
        <span
          title={t("web.player.routeBadge.locationTitle", { location: session.stream_location })}
          aria-label={t("web.player.routeBadge.locationAria", {
            location: session.stream_location,
          })}
          className="border-primary/20 bg-primary/10 text-primary inline-flex items-center rounded border px-1.5 py-0.5 text-[9px] font-semibold"
        >
          {session.stream_location === "local"
            ? t("web.player.routeBadge.local")
            : t("web.player.routeBadge.remote")}
        </span>
      )}
      {provider ? (
        <OverlayNetworkBadge provider={provider} />
      ) : (
        <NetworkBadge
          name={
            provider === ""
              ? t("web.player.routeBadge.defaultNetwork")
              : t("web.player.routeBadge.unknownNetwork")
          }
          title={
            provider === ""
              ? t("web.player.routeBadge.defaultNetworkTitle")
              : t("web.player.routeBadge.unknownNetworkTitle")
          }
        />
      )}
      {getSessionRouteNodes(session).map((node) => {
        const serves = session.routing_egress === "api" ? "yes" : "no";
        const title =
          node.kind !== "server"
            ? t("web.player.routeBadge.nodeTitle", { label: node.label, name: node.name })
            : session.reporting_node
              ? t("web.player.routeBadge.apiServerNodeTitle", {
                  serves,
                  node: session.reporting_node,
                })
              : t("web.player.routeBadge.apiServerTitle", { serves });
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
                <span aria-hidden="true">{SEPARATOR}</span>
              </>
            ) : null}
            <span className="truncate">
              {node.kind === "server" ? t("web.player.routeBadge.apiServer") : node.name}
            </span>
          </span>
        );
      })}
    </>
  );
}
