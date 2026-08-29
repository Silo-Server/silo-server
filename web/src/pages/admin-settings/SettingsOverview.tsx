import { CircleCheck } from "lucide-react";

import { useSettingsOverview } from "@/hooks/admin/useSettingsOverview";
import { HealthTile, HealthTileSkeleton, SectionCard, SectionCardSkeleton } from "./overviewCards";

const SKELETON_TILES = [0, 1];
const SKELETON_CARDS = [0, 1, 2, 3, 4, 5];

/**
 * The settings landing page: whatever needs the admin across the top, then one
 * directory of settings categories. Mounted at the settings index route.
 */
export default function SettingsOverview() {
  const { isLoading, tiles, cards } = useSettingsOverview();

  // Keep this focused on actionable setup and health items rather than
  // presenting every configured integration as a wall of green tiles.
  const attentionTiles = tiles.filter((tile) => tile.state === "warn" || tile.state === "off");

  return (
    <div className="w-full space-y-10">
      <header className="max-w-3xl space-y-3">
        <h1 className="page-title text-[clamp(2.25rem,4vw,3.5rem)]">Settings</h1>
        <p className="text-muted-foreground text-sm leading-relaxed sm:text-base">
          Configure the server, media processing, integrations, and the defaults your household
          starts with.
        </p>
      </header>

      <section aria-labelledby="setup-health-heading" className="space-y-4">
        <div className="space-y-1.5">
          <h2 id="setup-health-heading" className="text-xl font-semibold tracking-tight">
            Setup &amp; health
          </h2>
          <p className="text-muted-foreground text-sm">
            Recommendations and configuration problems that may limit server features.
          </p>
        </div>
        {isLoading ? (
          <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-5">
            {SKELETON_TILES.map((index) => (
              <HealthTileSkeleton key={index} />
            ))}
          </div>
        ) : attentionTiles.length === 0 ? (
          <div className="border-border/60 bg-card/35 inline-flex max-w-xl items-start gap-3 rounded-xl border px-4 py-3">
            <CircleCheck className="mt-0.5 size-4 shrink-0 text-emerald-500" aria-hidden="true" />
            <div className="space-y-0.5">
              <p className="text-sm font-medium">No action needed</p>
              <p className="text-muted-foreground text-xs leading-relaxed">
                Missing setup, unavailable services, and restart-required changes will appear here.
              </p>
            </div>
          </div>
        ) : (
          <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-5">
            {attentionTiles.map((tile) => (
              <HealthTile key={tile.id} tile={tile} />
            ))}
          </div>
        )}
      </section>

      <section aria-labelledby="settings-groups-heading" className="space-y-5">
        <div className="space-y-1.5">
          <h2 id="settings-groups-heading" className="text-xl font-semibold tracking-tight">
            Settings groups
          </h2>
          <p className="text-muted-foreground text-sm">
            Each group shows the sections you’ll find inside.
          </p>
        </div>
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {isLoading
            ? SKELETON_CARDS.map((index) => <SectionCardSkeleton key={index} />)
            : cards.map((card) => <SectionCard key={card.id} card={card} />)}
        </div>
      </section>
    </div>
  );
}
