import { useEffect } from "react";
import { Link, Navigate, useSearchParams } from "react-router";
import { Settings2 } from "lucide-react";
import { toast } from "sonner";
import { PageHeader } from "@/components/page/PageHeader";
import { Button } from "@/components/ui/button";
import { useAdminRequestCapabilities } from "@/hooks/queries/admin/requests";
import { useT } from "@/i18n";
import { RequestQueue } from "@/pages/admin-requests/RequestQueue";
import { EmptyPanel, RowsSkeleton } from "@/pages/admin-requests/queueParts";

/** Where request settings, servers, and routing live now. */
export const REQUEST_SETTINGS_HREF = "/admin/settings/requests";

// Tabs this page used to have. Settings and servers moved to Settings →
// Requests; per-account overrides moved to each account's page, next to its
// access group's, so the old link lands on the accounts list.
const MOVED_TABS: Record<string, string> = {
  settings: REQUEST_SETTINGS_HREF,
  integrations: REQUEST_SETTINGS_HREF,
  overrides: "/admin/users",
};

const OVERRIDES_MOVED_TOAST = "request-overrides-moved";

export default function AdminRequests() {
  const [searchParams] = useSearchParams();
  const requestedTab = searchParams.get("tab");
  const movedTo = requestedTab === null ? undefined : MOVED_TABS[requestedTab];
  if (movedTo) return <MovedTab tab={requestedTab!} to={movedTo} />;
  return <RequestQueuePage />;
}

/** Sends a link to a retired tab where its content lives now. */
function MovedTab({ tab, to }: { tab: string; to: string }) {
  const t = useT();
  useEffect(() => {
    if (tab !== "overrides") return;
    // The fixed id keeps a double-run effect from stacking two toasts.
    toast.info(t("web.admin.requests.page.overridesMoved.title"), {
      id: OVERRIDES_MOVED_TOAST,
      description: t("web.admin.requests.page.overridesMoved.description"),
    });
  }, [tab, t]);
  return <Navigate to={to} replace />;
}

function RequestQueuePage() {
  const t = useT();
  const capabilities = useAdminRequestCapabilities();
  if (capabilities.isLoading) return <RowsSkeleton />;
  if (!capabilities.data?.available) {
    return (
      <EmptyPanel
        title={t("web.admin.requests.page.unavailable.title")}
        detail={t("web.admin.requests.page.unavailable.detail")}
      />
    );
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.admin.requests.page.title")}
        subtitle={t("web.admin.requests.page.subtitle")}
        actions={
          <Button asChild variant="outline" size="sm">
            <Link to={REQUEST_SETTINGS_HREF}>
              <Settings2 aria-hidden="true" />
              {t("web.admin.requests.page.settingsLink")}
            </Link>
          </Button>
        }
      />

      <RequestQueue />
    </div>
  );
}
