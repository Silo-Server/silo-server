import { useId } from "react";

import type { RequestIntegration } from "@/api/types";
import type {
  RequestRoute,
  RequestRouteMediaType,
  RequestRouting,
  RequestRoutingMode,
} from "@/api/v2/adminRequests";
import { ChoiceCard } from "@/pages/admin/autoscan/ChoiceCard";

import { serverKind } from "./requestServerModel";

const MEDIA_TYPES: readonly { value: RequestRouteMediaType; label: string; kind: string }[] = [
  { value: "movie", label: "Movies", kind: "Radarr" },
  { value: "series", label: "Series", kind: "Sonarr" },
];

/** Standard or Advanced. Standard is offered only while the servers allow it. */
export function RoutingModeChoice({
  routing,
  pending,
  onChange,
}: {
  routing: RequestRouting;
  pending: boolean;
  onChange: (mode: RequestRoutingMode) => void;
}) {
  const blocked = Boolean(routing.standard_unavailable_reason);
  const reasonId = useId();
  const showReason = blocked && routing.mode === "advanced";
  return (
    <div className="space-y-2">
      <div role="group" aria-label="Routing" className="grid gap-2 sm:grid-cols-2">
        <ChoiceCard
          title="Standard"
          description="Each request goes to the server for its type, with that server's own settings."
          selected={routing.mode === "standard"}
          disabled={pending || (blocked && routing.mode !== "standard")}
          describedBy={showReason ? reasonId : undefined}
          onSelect={() => routing.mode !== "standard" && onChange("standard")}
        />
        <ChoiceCard
          title="Advanced"
          description="Rules send some requests elsewhere, like anime to a second Sonarr or kids' movies to their own folder."
          selected={routing.mode === "advanced"}
          disabled={pending}
          onSelect={() => routing.mode !== "advanced" && onChange("advanced")}
        />
      </div>
      {showReason ? (
        <p id={reasonId} className="text-muted-foreground text-xs">
          Standard isn&apos;t available. {routing.standard_unavailable_reason}
        </p>
      ) : null}
    </div>
  );
}

/**
 * What Standard does, one line per media type: its server, and where its 4K
 * copies go.
 */
export function StandardRoutingSummary({
  routing,
  servers,
  routes,
}: {
  routing: RequestRouting;
  servers: readonly RequestIntegration[];
  routes: readonly RequestRoute[];
}) {
  const name = (id: string | undefined) => servers.find((server) => server.id === id)?.name;
  const pausedRules = routes.filter((route) => !route.is_fallback && route.enabled).length;
  const isArr = (id: string | undefined) => {
    const server = servers.find((candidate) => candidate.id === id);
    return server !== undefined && serverKind(server) !== "";
  };
  // The 4K hint matters only while a Radarr or Sonarr media type has no 4K
  // server; another plugin (Seerr) handles 4K itself.
  const missing4K = routing.standard.some(
    (d) => isArr(d.hd_integration_id) && !d.uhd_integration_id,
  );
  if (routing.standard_unavailable_reason) {
    // Saved as Standard while a server change was being made: the rules decide
    // until an admin picks.
    return (
      <p className="text-muted-foreground pt-3 text-sm">
        Standard can&apos;t route requests right now. {routing.standard_unavailable_reason} Until
        you switch to Advanced or remove a server, the rules decide.
      </p>
    );
  }
  return (
    <div className="space-y-3 pt-3">
      <ul aria-label="Where Standard sends requests" className="space-y-2">
        {MEDIA_TYPES.map((type) => {
          const destination = routing.standard.find((d) => d.media_type === type.value);
          const hd = name(destination?.hd_integration_id);
          const uhd = name(destination?.uhd_integration_id);
          return (
            <li key={type.value} className="border-border/70 rounded-lg border px-3 py-2 text-sm">
              <span className="font-medium">{type.label}</span>
              {hd ? (
                <>
                  {" → "}
                  {hd}
                </>
              ) : uhd ? (
                <span className="text-muted-foreground">
                  {" "}
                  — only a 4K server, so HD versions go nowhere. Add a {type.kind} server that
                  isn&apos;t marked 4K.
                </span>
              ) : (
                <span className="text-muted-foreground">
                  {" "}
                  — no server yet. Add a {type.kind} server above.
                </span>
              )}
              {hd || uhd ? (
                <span className="text-muted-foreground block text-xs">
                  {uhd ? `4K versions → ${uhd}` : "No 4K versions"}
                </span>
              ) : null}
            </li>
          );
        })}
      </ul>
      {routing.standard.some((d) => d.media_type === "series" && isArr(d.hd_integration_id)) ? (
        <p className="text-muted-foreground text-xs">
          Anime series go to Sonarr with the Anime series type, so episodes are numbered the way
          anime releases are.
        </p>
      ) : null}
      {missing4K ? (
        <p className="text-muted-foreground text-xs">
          To send 4K versions to their own server, add it and turn on &ldquo;4K server&rdquo;.
          Adding a second server of the same type turns on Advanced.
        </p>
      ) : null}
      {pausedRules > 0 ? (
        <p className="text-muted-foreground text-xs">
          {pausedRules === 1 ? "1 rule is" : `${pausedRules} rules are`} paused. Switch to Advanced
          to use {pausedRules === 1 ? "it" : "them"} again.
        </p>
      ) : null}
    </div>
  );
}
