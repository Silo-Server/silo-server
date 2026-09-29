import { useEffect } from "react";
import { Link, Navigate, useSearchParams } from "react-router";
import { Settings2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { useAdminRequestCapabilities } from "@/hooks/queries/admin/requests";
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
  useEffect(() => {
    if (tab !== "overrides") return;
    // The fixed id keeps a double-run effect from stacking two toasts.
    toast.info("Request limits moved to each account", {
      id: OVERRIDES_MOVED_TOAST,
      description:
        "Open an account to set its request approval and limit. Access groups set them for everyone in the group.",
    });
  }, [tab]);
  return <Navigate to={to} replace />;
}

function RequestQueuePage() {
  const capabilities = useAdminRequestCapabilities();
  if (capabilities.isLoading) return <RowsSkeleton />;
  if (!capabilities.data?.available) {
    return (
      <EmptyPanel
        title="Request administration unavailable"
        detail="Request administration could not be enabled on this server."
      />
    );
  }

  return (
    <div className="space-y-6">
      {/* The action sits under the text, as on the other admin pages: the
          shell's search button floats over the header's top-right corner. */}
      <div className="page-header">
        <div className="space-y-2">
          <h1 className="text-3xl font-semibold tracking-normal text-balance sm:text-4xl">
            Requests
          </h1>
          <p className="text-muted-foreground max-w-2xl text-sm leading-6">
            Approve, decline, and follow media requests.
          </p>
          <Button asChild variant="outline" size="sm">
            <Link to={REQUEST_SETTINGS_HREF}>
              <Settings2 aria-hidden="true" />
              Request settings
            </Link>
          </Button>
        </div>
      </div>

      <RequestQueue />
    </div>
  );
}
