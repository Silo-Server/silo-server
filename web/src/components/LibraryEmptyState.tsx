import { FolderOpen, Loader2 } from "lucide-react";
import { Link } from "react-router";

import { Button } from "@/components/ui/button";
import { useActiveScans } from "@/hooks/queries/admin/scans";
import { useScanLibrary } from "@/hooks/queries/admin/scanControls";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";

interface LibraryEmptyStateProps {
  libraryId: number;
}

/**
 * Shown when a library holds no items at all, as opposed to a view whose
 * filters match nothing. Admins get the scan action and a link to library
 * management; everyone else gets a plain message. Active scans only reach
 * admins (the `scans` realtime channel is admin-only), so only they see the
 * scanning variant.
 */
export default function LibraryEmptyState({ libraryId }: LibraryEmptyStateProps) {
  const actingAdmin = useIsActingAdmin();
  const { data: activeScans = [] } = useActiveScans();
  const scanning =
    actingAdmin &&
    activeScans.some(
      (scan) =>
        scan.library_id === libraryId && (scan.status === "accepted" || scan.status === "running"),
    );

  return (
    <div
      data-testid="library-empty-state"
      className="surface-panel flex min-h-64 flex-col items-center justify-center gap-3 rounded-[1.8rem] border-0 px-6 py-10 text-center"
    >
      {scanning ? (
        <>
          <Loader2 className="text-muted-foreground h-10 w-10 animate-spin" aria-hidden="true" />
          <div className="space-y-1">
            <p className="text-sm font-medium">Scanning this library</p>
            <p className="text-muted-foreground max-w-sm text-sm">
              Titles will appear here as the scan finds them.
            </p>
          </div>
        </>
      ) : (
        <>
          <FolderOpen className="text-muted-foreground h-10 w-10" aria-hidden="true" />
          <div className="space-y-1">
            <p className="text-sm font-medium">This library is empty</p>
            <p className="text-muted-foreground max-w-sm text-sm">
              {actingAdmin
                ? "No media has been added yet. Add files to the library's folders, then scan it."
                : "There is nothing in this library yet."}
            </p>
          </div>
          {actingAdmin ? <AdminEmptyLibraryActions libraryId={libraryId} /> : null}
        </>
      )}
    </div>
  );
}

function AdminEmptyLibraryActions({ libraryId }: { libraryId: number }) {
  const scanLibrary = useScanLibrary();

  return (
    <div className="flex flex-wrap items-center justify-center gap-4">
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={scanLibrary.isPending}
        onClick={() => scanLibrary.mutate(libraryId)}
      >
        Scan library
      </Button>
      <Link to="/admin/libraries" className="text-primary text-sm font-medium hover:underline">
        Manage libraries
      </Link>
    </div>
  );
}
