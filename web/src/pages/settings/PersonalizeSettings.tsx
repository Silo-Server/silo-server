import { useState } from "react";
import { Link } from "react-router";
import { Sparkles, ArrowRight, Heart, Map } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useFavorites } from "@/hooks/queries/favorites";
import { useOnboardingFlow } from "@/hooks/queries/onboarding";
import { TourHost } from "@/components/onboarding/TourHost";

/**
 * Re-entry point for the taste-seeding flow. Always links to /taste-seed —
 * the page itself handles already-favorited items by pre-marking them, so
 * users can both add new picks and review their existing ones from here.
 */
export default function PersonalizeSettings() {
  const { data: favorites } = useFavorites();
  const favoriteCount = favorites?.length ?? 0;
  const [replaying, setReplaying] = useState(false);
  // Fetch lazily: the flow only loads when the user asks for a replay.
  const flow = useOnboardingFlow({ enabled: replaying });

  return (
    <div className="space-y-6">
      <header className="space-y-2">
        <h2 className="text-foreground text-xl font-semibold tracking-tight">Personalize</h2>
        <p className="text-muted-foreground text-sm">
          Pick titles you love so your home, recommendations, and "For You" rows reflect your taste.
        </p>
      </header>

      <div className="surface-panel rounded-2xl border-0 p-6">
        <div className="flex items-start gap-4">
          <div className="bg-primary/10 text-primary flex h-12 w-12 shrink-0 items-center justify-center rounded-full">
            <Sparkles className="h-6 w-6" />
          </div>
          <div className="min-w-0 flex-1 space-y-3">
            <div>
              <h3 className="text-base font-semibold">Refine your taste profile</h3>
              <p className="text-muted-foreground mt-1 text-sm">
                Browse popular titles and pick the ones you love. Already-favorited titles will be
                marked, so you can add to (or trim) your picks any time.
              </p>
            </div>
            <Button asChild>
              <Link to="/taste-seed?from=settings">
                Open the picker
                <ArrowRight className="ml-1 h-4 w-4" />
              </Link>
            </Button>
          </div>
        </div>
      </div>

      <div className="surface-panel rounded-2xl border-0 p-6">
        <div className="flex items-start gap-4">
          <div className="bg-primary/10 text-primary flex h-12 w-12 shrink-0 items-center justify-center rounded-full">
            <Map className="h-6 w-6" />
          </div>
          <div className="min-w-0 flex-1 space-y-3">
            <div>
              <h3 className="text-base font-semibold">Replay the feature tour</h3>
              <p className="text-muted-foreground mt-1 text-sm">
                A two-minute walkthrough of what this server can do — watch together, requests,
                watchlists, and the playback settings worth knowing about.
              </p>
            </div>
            <Button variant="outline" onClick={() => setReplaying(true)}>
              Start the tour
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
            ? "You haven't favorited anything yet."
            : favoriteCount === 1
              ? "1 favorite is shaping your recommendations."
              : `${favoriteCount} favorites are shaping your recommendations.`}
        </span>
      </div>
    </div>
  );
}
