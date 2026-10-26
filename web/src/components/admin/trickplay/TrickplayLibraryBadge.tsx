import { AlertTriangle } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import type { AdminTrickplayLibrary } from "@/hooks/queries/admin/trickplay";

import { libraryTrickplayDetail, libraryTrickplayLabel } from "./trickplayStatus";

/** The libraries list's seek-preview badge; renders nothing for a library that makes none. */
export function TrickplayLibraryBadge({ library }: { library: AdminTrickplayLibrary | undefined }) {
  if (!library) return null;
  const detail = libraryTrickplayDetail(library);
  return (
    <Badge
      variant="outline"
      className={library.unusable > 0 ? "border-warning/40 text-warning" : undefined}
      title={detail}
    >
      {library.unusable > 0 ? <AlertTriangle aria-hidden="true" /> : null}
      {libraryTrickplayLabel(library)}
      <span className="sr-only">. {detail}</span>
    </Badge>
  );
}
