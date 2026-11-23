import { useState } from "react";
import { Link } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { Loader2, Plus } from "lucide-react";
import { toast } from "sonner";
import { isRequestEditorConflict, putAdminDiscoverLayoutV2 } from "@/api/v2/adminRequests";
import { Button } from "@/components/ui/button";
import { useAdminRequestCapabilities } from "@/hooks/queries/admin/requests";
import { useRequestDiscoverLayout } from "@/hooks/queries/admin/requestDiscover";
import { adminKeys, requestKeys } from "@/hooks/queries/keys";
import { useRequestFeatureStatus } from "@/hooks/queries/useRequests";
import { useT } from "@/i18n";
import { withRecommendedDiscoverRows } from "@/pages/admin-requests/discoverLayoutModel";

/**
 * Prerequisite and Discover-layout callouts inside Admin → Recommendations →
 * Discover Recommendations. Helps admins spot off prerequisites and add
 * recommended rows to a customized Discover page in one click.
 */
export function RecExternalSectionNotices({
  recsEnabled,
  externalEnabled,
}: {
  recsEnabled: boolean;
  externalEnabled: boolean;
}) {
  const t = useT();
  const queryClient = useQueryClient();
  const [adding, setAdding] = useState(false);

  const requestStatus = useRequestFeatureStatus().data;
  const requestsEnabled = requestStatus?.requests_enabled;
  const checkLayout = externalEnabled && requestsEnabled === true;
  const capabilities = useAdminRequestCapabilities(checkLayout);
  const discoverLayout = useRequestDiscoverLayout(
    checkLayout && capabilities.data?.discover_layout === true,
  );

  const layout = discoverLayout.data;
  const hasEnabledRecommended =
    layout?.rows.some((row) => row.type === "recommended" && row.enabled) ?? true;
  const nextRows = layout && !hasEnabledRecommended ? withRecommendedDiscoverRows(layout) : null;
  const showMissingFromDiscover = checkLayout && layout !== undefined && !hasEnabledRecommended;

  const showNeedsRecommendations = !recsEnabled;
  const showNeedsRequests = requestsEnabled === false;

  if (!showNeedsRecommendations && !showNeedsRequests && !showMissingFromDiscover) {
    return null;
  }

  async function handleAddToDiscover() {
    if (!layout || !nextRows || adding) return;
    setAdding(true);
    try {
      await queryClient.cancelQueries({ queryKey: adminKeys.requestDiscoverLayout() });
      const saved = await putAdminDiscoverLayoutV2(nextRows, layout);
      queryClient.setQueryData(adminKeys.requestDiscoverLayout(), saved);
      void queryClient.invalidateQueries({ queryKey: requestKeys.discovery() });
      toast.success(t("web.admin.settings.recommendations.external.addedToDiscover"));
    } catch (error) {
      if (isRequestEditorConflict(error)) {
        void queryClient.invalidateQueries({ queryKey: adminKeys.requestDiscoverLayout() });
      }
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t("web.admin.settings.recommendations.external.addToDiscoverFailed"),
      );
    } finally {
      setAdding(false);
    }
  }

  return (
    <div className="space-y-2 py-3 text-sm">
      {showNeedsRecommendations && (
        <p className="text-warning text-xs">
          {t("web.admin.settings.recommendations.external.needsRecommendations")}
        </p>
      )}
      {showNeedsRequests && (
        <p className="text-muted-foreground text-xs">
          {t("web.admin.settings.recommendations.external.needsRequests", {
            link: (chunks) => (
              <Link
                to="/admin/requests"
                className="text-foreground font-medium underline underline-offset-2"
              >
                {chunks}
              </Link>
            ),
          })}
        </p>
      )}
      {showMissingFromDiscover && (
        <div className="bg-muted/40 flex flex-wrap items-center justify-between gap-3 rounded-xl px-3.5 py-2.5 text-xs">
          <p className="text-muted-foreground min-w-0 flex-1">
            {t("web.admin.settings.recommendations.external.missingFromDiscover")}
          </p>
          <div className="flex shrink-0 items-center gap-2">
            {nextRows && (
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={adding}
                onClick={() => void handleAddToDiscover()}
              >
                {adding ? (
                  <Loader2 className="size-3.5 animate-spin" />
                ) : (
                  <Plus className="size-3.5" />
                )}
                {t("web.admin.settings.recommendations.external.addToDiscover")}
              </Button>
            )}
            <Link
              to="/admin/requests/discover"
              className="text-foreground font-medium underline underline-offset-2"
            >
              {t("web.admin.settings.recommendations.external.openDiscoverPage")}
            </Link>
          </div>
        </div>
      )}
    </div>
  );
}
