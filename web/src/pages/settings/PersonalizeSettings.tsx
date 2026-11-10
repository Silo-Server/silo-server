import { useState } from "react";
import { Link } from "react-router";
import { Sparkles, ArrowRight, Heart, Map } from "lucide-react";
import { PageHeader } from "@/components/page/PageHeader";
import { Button } from "@/components/ui/button";
import { useFavorites } from "@/hooks/queries/favorites";
import { useOnboardingFlow } from "@/hooks/queries/onboarding";
import { TourHost } from "@/components/onboarding/TourHost";
import { NotInterestedList } from "@/components/settings/NotInterestedList";
import { useT } from "@/i18n";

/**
 * Re-entry point for the taste-seeding flow. Always links to /taste-seed —
 * the page itself handles already-favorited items by pre-marking them, so
 * users can both add new picks and review their existing ones from here.
 */
export default function PersonalizeSettings() {
  const t = useT();
  const { data: favorites } = useFavorites();
  const favoriteCount = favorites?.length ?? 0;
  const [replaying, setReplaying] = useState(false);
  // Fetch lazily: the flow only loads when the user asks for a replay.
  const flow = useOnboardingFlow({ enabled: replaying });

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.settings.personalize.title")}
        subtitle={t("web.settings.personalize.subtitle")}
      />

      <div className="surface-panel rounded-2xl border-0 p-6">
        <div className="flex items-start gap-4">
          <div className="bg-primary/10 text-primary flex h-12 w-12 shrink-0 items-center justify-center rounded-full">
            <Sparkles className="h-6 w-6" />
          </div>
          <div className="min-w-0 flex-1 space-y-3">
            <div>
              <h3 className="text-base font-semibold">
                {t("web.settings.personalize.refineTitle")}
              </h3>
              <p className="text-muted-foreground mt-1 text-sm">
                {t("web.settings.personalize.refineDescription")}
              </p>
            </div>
            <Button asChild>
              <Link to="/taste-seed?from=settings">
                {t("web.settings.personalize.openPicker")}
                <ArrowRight className="ms-1 h-4 w-4" />
              </Link>
            </Button>
          </div>
        </div>
      </div>

      <NotInterestedList />

      <div className="surface-panel rounded-2xl border-0 p-6">
        <div className="flex items-start gap-4">
          <div className="bg-primary/10 text-primary flex h-12 w-12 shrink-0 items-center justify-center rounded-full">
            <Map className="h-6 w-6" />
          </div>
          <div className="min-w-0 flex-1 space-y-3">
            <div>
              <h3 className="text-base font-semibold">{t("web.settings.personalize.tourTitle")}</h3>
              <p className="text-muted-foreground mt-1 text-sm">
                {t("web.settings.personalize.tourDescription")}
              </p>
            </div>
            <Button variant="outline" onClick={() => setReplaying(true)}>
              {t("web.settings.personalize.startTour")}
            </Button>
          </div>
        </div>
      </div>

      {replaying && flow.data && flow.data.steps.length > 0 && (
        <TourHost flow={flow.data} onDone={() => setReplaying(false)} />
      )}

      <div className="text-muted-foreground flex items-center gap-2 px-2 text-sm">
        <Heart className="h-4 w-4" />
        <span>
          {favoriteCount === 0
            ? t("web.settings.personalize.noFavorites")
            : t("web.settings.personalize.favoriteCount", {
                count: favoriteCount,
                count_text: favoriteCount,
              })}
        </span>
      </div>
    </div>
  );
}
