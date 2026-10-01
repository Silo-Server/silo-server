import { useId, useState } from "react";
import { ChevronRight } from "lucide-react";

import type { RequestIntegration } from "@/api/types";
import type {
  RequestRoute,
  RequestRoutePreview as RoutePreview,
  RequestRoutePreviewTier,
  RequestRouteTitle,
} from "@/api/v2/adminRequests";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useDebounce } from "@/hooks/useDebounce";
import {
  useAdminRequestRoutePreview,
  useRequestIntegrationOptions,
  useRequestRouteTitles,
} from "@/hooks/queries/admin/requests";

import type { RoutingScope } from "./RequestRuleEditor";
import {
  destinationSentence,
  factsSentence,
  SERVER_NAMED_OVERRIDES,
  serverOverrideFields,
  traceLine,
  type RoutingNames,
} from "./requestRoutingModel";
import type { RequestRouterInstallation } from "./requestServerModel";

const MAX_RESULTS = 6;
const ANYONE = "__anyone__";

/**
 * Standard routing's route: the server makes one per media type for the
 * preview, not stored and not in the list.
 */
function isStandardRoute(routeId: string | undefined): boolean {
  return Boolean(routeId?.startsWith("standard-"));
}

/** Which rule decided a tier, as "rule 1, Anime", "Everything else" or "Standard". */
function deciderLabel(preview: RoutePreview, routeId: string | undefined): string | undefined {
  if (!routeId) return undefined;
  if (isStandardRoute(routeId)) return "Standard";
  const rules = preview.rules.filter((rule) => !rule.is_fallback);
  const index = rules.findIndex((rule) => rule.route_id === routeId);
  if (index !== -1) return `rule ${index + 1}, ${rules[index]!.route_name}`;
  const fallback = preview.rules.find((rule) => rule.route_id === routeId);
  return fallback?.is_fallback ? "Everything else" : undefined;
}

function TierLine({
  label,
  tier,
  preview,
  servers,
  installations,
}: {
  label: string;
  tier: RequestRoutePreviewTier;
  preview: RoutePreview;
  servers: readonly RequestIntegration[];
  installations: RequestRouterInstallation[];
}) {
  const server = servers.find((candidate) => candidate.id === tier.integration_id);
  const needsNames = Object.keys(tier.overrides ?? {}).some((key) =>
    SERVER_NAMED_OVERRIDES.has(key),
  );
  const options = useRequestIntegrationOptions(needsNames && server ? server.id : undefined);
  const fields = server ? serverOverrideFields(server, installations) : [];
  const decider = deciderLabel(preview, tier.route_id);
  let outcome: string;
  if (tier.integration_id) {
    const where = destinationSentence(
      { integration_id: tier.integration_id, overrides: tier.overrides },
      server ? servers : [{ id: tier.integration_id, name: tier.integration_name ?? "" }],
      { options: options.data, fields },
    );
    outcome = decider ? `${where} (${decider})` : where;
  } else if (isStandardRoute(tier.route_id)) {
    outcome = "none (no server is marked 4K)";
  } else if (tier.route_id) {
    outcome =
      decider === "Everything else"
        ? "none (Everything else doesn't send 4K versions)"
        : `none (${decider ?? tier.route_name} doesn't send 4K versions)`;
  } else {
    outcome = "not sent";
  }
  return (
    <li className="space-y-0.5">
      <p>
        <span className="font-medium">{label} →</span> {outcome}
      </p>
      {tier.note && (tier.integration_id || !tier.route_id) ? (
        <p
          className={
            tier.integration_id
              ? "text-xs text-amber-600 dark:text-amber-400"
              : "text-muted-foreground text-xs"
          }
        >
          {tier.note}
        </p>
      ) : null}
    </li>
  );
}

/**
 * Where each copy of a request for a title goes and why: the title's facts,
 * one line per copy, and (open or collapsed) how each rule decided.
 */
export function RoutePreviewResult({
  preview,
  title,
  mediaType,
  servers,
  installations,
  routes,
  names,
  requesterUserId,
  traceOpen = true,
}: {
  preview: RoutePreview;
  /** "Spirited Away (2001)"; omitted where the title is already shown. */
  title?: string;
  mediaType: RequestRoute["media_type"];
  servers: readonly RequestIntegration[];
  installations: RequestRouterInstallation[];
  /** The routes as listed, for each rule's conditions in the explanation. */
  routes: readonly RequestRoute[];
  names: RoutingNames;
  requesterUserId?: number;
  traceOpen?: boolean;
}) {
  const [open, setOpen] = useState(traceOpen);
  const traceId = useId();
  const tiers: Record<string, string> = { "1080p": "HD version", "2160p": "4K version" };
  // Rules are numbered as the list numbers them; Everything else is not.
  const numbers = preview.rules.map(
    (_, index) => preview.rules.slice(0, index + 1).filter((rule) => !rule.is_fallback).length,
  );
  return (
    <div className="space-y-2">
      {title ? <p className="font-medium">{title}</p> : null}
      <p className="text-muted-foreground text-xs">{factsSentence(preview.facts, mediaType)}</p>
      <ul className="list-none space-y-1">
        {preview.tiers.map((tier) => (
          <TierLine
            key={tier.quality}
            label={tiers[tier.quality] ?? tier.quality}
            tier={tier}
            preview={preview}
            servers={servers}
            installations={installations}
          />
        ))}
      </ul>
      {/* Under Standard one route decides everything; there is nothing to explain. */}
      {preview.rules.some((rule) => !isStandardRoute(rule.route_id)) ? (
        <div>
          <button
            type="button"
            aria-expanded={open}
            aria-controls={traceId}
            onClick={() => setOpen((current) => !current)}
            className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs font-medium"
          >
            <ChevronRight
              className={`size-3.5 transition-transform ${open ? "rotate-90" : ""}`}
              aria-hidden="true"
            />
            How it was decided
          </button>
          {open ? (
            <ol
              id={traceId}
              className="text-muted-foreground mt-1 list-none space-y-0.5 pl-5 text-xs"
            >
              {preview.rules.map((rule, index) => {
                const route = routes.find((candidate) => candidate.id === rule.route_id);
                return (
                  <li key={rule.route_id}>
                    {traceLine(rule, numbers[index]!, route?.conditions, {
                      facts: preview.facts,
                      mediaType,
                      names,
                      requesterUserId,
                    })}
                  </li>
                );
              })}
            </ol>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/**
 * Try a title: search TMDB, pick who asks, and see which rule a request
 * would match and where each copy would go, with the rules as saved. Works
 * whether or not requests are turned on.
 */
export function RequestRoutePreview({ scope }: { scope: RoutingScope }) {
  const { mediaType } = scope;
  const plural = mediaType === "series" ? "series" : "movies";
  const requesterId = useId();
  const [query, setQuery] = useState("");
  const [picked, setPicked] = useState<RequestRouteTitle | null>(null);
  const [requester, setRequester] = useState(ANYONE);
  const debounced = useDebounce(query, 300);
  const search = useRequestRouteTitles(mediaType, debounced, { enabled: picked === null });
  const requesterUserId = requester === ANYONE ? undefined : Number(requester);
  const preview = useAdminRequestRoutePreview(
    picked ? { mediaType, tmdbId: picked.tmdb_id, requesterUserId } : null,
  );
  const results = (search.data ?? []).slice(0, MAX_RESULTS);
  const searching = picked === null && debounced.trim().length > 1;
  const routes = [...scope.rules, ...(scope.fallback ? [scope.fallback] : [])];

  return (
    <div className="pb-3.5">
      <SettingsSubheading caption="See which rule a request would match and where each version would go, with the rules as saved.">
        Try a title
      </SettingsSubheading>
      <div className="flex flex-col gap-3 pt-2 sm:flex-row sm:items-start">
        <Input
          type="search"
          aria-label={`Search ${plural}`}
          placeholder={`Search ${plural}`}
          value={query}
          onChange={(event) => {
            setQuery(event.target.value);
            setPicked(null);
          }}
          className="sm:flex-1"
        />
        <div className="flex shrink-0 flex-col gap-1">
          <div className="flex items-center gap-2 sm:justify-end">
            <label htmlFor={requesterId} className="text-muted-foreground shrink-0 text-xs">
              Requested by
            </label>
            <Select value={requester} onValueChange={setRequester}>
              <SelectTrigger id={requesterId} className="w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ANYONE}>Anyone</SelectItem>
                {scope.lookups.users.map((user) => (
                  <SelectItem key={user.id} value={String(user.id)}>
                    {user.username}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <p className="text-muted-foreground text-xs">
            Rules for certain people only match their requests.
          </p>
        </div>
      </div>

      {searching ? (
        search.isLoading ? (
          <p className="text-muted-foreground pt-2 text-xs">Searching…</p>
        ) : search.isError ? (
          <p className="text-destructive pt-2 text-xs">
            {search.error instanceof Error ? search.error.message : "Search failed."}
          </p>
        ) : results.length === 0 ? (
          <p className="text-muted-foreground pt-2 text-xs">No matches.</p>
        ) : (
          <ul
            aria-label="Matching titles"
            className="flex list-none flex-col items-start gap-0.5 pt-2"
          >
            {results.map((result) => (
              <li key={result.tmdb_id}>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setQuery(result.title);
                    setPicked(result);
                  }}
                >
                  {result.title}
                  {result.year ? (
                    <span className="text-muted-foreground">({result.year})</span>
                  ) : null}
                </Button>
              </li>
            ))}
          </ul>
        )
      ) : null}

      {picked ? (
        <div
          aria-live="polite"
          className="border-border/70 bg-foreground/[0.02] mt-3 space-y-2 rounded-xl border p-3 text-sm"
        >
          {preview.isPending ? (
            <p className="text-muted-foreground text-xs">Checking…</p>
          ) : preview.isError ? (
            <p className="text-destructive text-xs">
              {preview.error instanceof Error ? preview.error.message : "The preview failed."}
            </p>
          ) : (
            <RoutePreviewResult
              preview={preview.data}
              title={picked.year ? `${picked.title} (${picked.year})` : picked.title}
              mediaType={mediaType}
              servers={scope.allServers}
              installations={scope.installations}
              routes={routes}
              names={scope.names}
              requesterUserId={requesterUserId}
            />
          )}
        </div>
      ) : null}
    </div>
  );
}
