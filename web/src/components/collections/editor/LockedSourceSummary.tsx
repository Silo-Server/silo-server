import { Lock } from "lucide-react";

import { discoverLocked, discoverStillEditable } from "@/lib/collections/copy";
import { syncedSourceLabel } from "@/lib/collections/types";
import { useT, type Translate } from "@/i18n";

/** The order a Discover list's `sort_by` sorts in, as a `discoverFindsOrdered` choice. */
const DISCOVER_ORDER: Record<string, string> = {
  "popularity.desc": "popular",
  "vote_average.desc": "rated",
  "vote_count.desc": "voted",
  "revenue.desc": "grossing",
  "primary_release_date.desc": "newest",
  "release_date.desc": "newest",
  "first_air_date.desc": "newest",
};

function text(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

/** "Movies, most popular first": what a Discover list finds. */
function discoverFinds(t: Translate, config: Record<string, unknown>): string {
  const discover = (config.discover ?? {}) as Record<string, unknown>;
  const media = text(config.media_type);
  const order = DISCOVER_ORDER[text(discover.sort_by)];
  return order
    ? t("web.collections.editor.discoverFindsOrdered", { media, order })
    : t("web.collections.editor.discoverFinds", { media });
}

/**
 * The list a Discover (starter pack) list follows, read-only: the editor
 * can't rebuild its source, so a save never sends it.
 */
export function LockedSourceSummary({ config }: { config: Record<string, unknown> }) {
  const t = useT();
  return (
    <div className="bg-muted/40 grid gap-3 rounded-xl px-4 py-3.5 text-[13.5px]">
      <p className="flex items-center gap-2 font-semibold">
        <Lock aria-hidden className="size-4 shrink-0" />
        {discoverLocked()}
      </p>
      <dl className="grid grid-cols-[72px_minmax(0,1fr)] gap-x-3 gap-y-1.5">
        <dt className="text-muted-foreground">{t("web.collections.editor.lockedSource")}</dt>
        <dd>{syncedSourceLabel("tmdb_discover")}</dd>
        <dt className="text-muted-foreground">{t("web.collections.editor.lockedFinds")}</dt>
        <dd className="break-words">{discoverFinds(t, config)}</dd>
      </dl>
      <p className="text-muted-foreground text-[13px]">{discoverStillEditable()}</p>
    </div>
  );
}
