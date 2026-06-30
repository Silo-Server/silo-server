import { useState } from "react";
import { Link } from "react-router";
import { Sparkles, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/hooks/useAuth";
import { useFavorites } from "@/hooks/queries/favorites";
import { USER_CAPABILITY_ACTIONS, useUserCapabilityAccess } from "@/hooks/useUserCapabilities";
import {
  isTasteSeedBannerDismissed,
  isTasteSeedDismissed,
  setTasteSeedBannerDismissed,
} from "@/lib/tasteSeed";

/**
 * Soft re-prompt for users who skipped the post-signup taste-seeding flow.
 * Renders only when:
 *  - the profile has no favorites yet (the "seeded" signal is inferred from
 *    favorites count — see lib/tasteSeed.ts for rationale)
 *  - the profile already dismissed the initial redirect (otherwise the gate
 *    would have sent them to /taste-seed instead of letting them reach Home)
 *  - the user hasn't explicitly dismissed this banner before
 *
 * Click "Personalize" → /taste-seed (with ?from=settings so the page Cancel
 * label is appropriate). X button hides the banner permanently for this
 * profile.
 */
export default function TasteSeedBanner() {
  const { profile } = useAuth();
  const userCapabilities = useUserCapabilityAccess();
  const canManagePersonalLists = userCapabilities.can(USER_CAPABILITY_ACTIONS.personalListsManage);
  const { data: favorites, isPending } = useFavorites();
  const [hidden, setHidden] = useState(false);

  if (userCapabilities.isLoading || !canManagePersonalLists) return null;
  if (!profile || isPending) return null;
  if ((favorites?.length ?? 0) > 0) return null;
  if (!isTasteSeedDismissed(profile.id)) return null;
  if (isTasteSeedBannerDismissed(profile.id) || hidden) return null;

  const handleDismiss = () => {
    setTasteSeedBannerDismissed(profile.id);
    setHidden(true);
  };

  return (
    <div className="px-4 sm:px-6 lg:px-10 xl:px-12">
      <div className="surface-panel relative flex flex-col items-start gap-3 rounded-[1.4rem] border-0 px-5 py-4 sm:flex-row sm:items-center sm:gap-4 sm:px-6">
        <div className="bg-primary/10 text-primary flex h-10 w-10 shrink-0 items-center justify-center rounded-full">
          <Sparkles className="h-5 w-5" aria-hidden="true" />
        </div>
        <div className="min-w-0 flex-1">
          <p className="text-sm font-semibold sm:text-base">Personalize your home</p>
          <p className="text-muted-foreground text-xs sm:text-sm">
            Pick a few titles you love and we'll tailor your recommendations.
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Button asChild size="sm">
            <Link to="/taste-seed?from=settings">Personalize</Link>
          </Button>
          <button
            type="button"
            onClick={handleDismiss}
            aria-label="Dismiss personalization prompt"
            className="text-muted-foreground hover:text-foreground inline-flex h-8 w-8 items-center justify-center rounded-full transition-colors"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
      </div>
    </div>
  );
}
