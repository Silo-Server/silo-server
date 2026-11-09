import { useId } from "react";

import type { UserLibrary } from "@/api/types";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";

/** The profile editor's Restrict libraries switch and, while it is on, a switch per library. */
export function LibraryRestrictionField({
  libraries,
  enabled,
  allowedLibraryIDs,
  error,
  onEnabledChange,
  onLibraryChange,
}: {
  libraries: UserLibrary[];
  enabled: boolean;
  allowedLibraryIDs: number[];
  error?: string;
  onEnabledChange: (checked: boolean) => void;
  onLibraryChange: (libraryID: number, checked: boolean) => void;
}) {
  const restrictLibrariesId = useId();
  return (
    <div className="border-border space-y-3 rounded-md border p-3">
      <div className="flex items-center justify-between gap-4">
        <div className="space-y-0.5">
          <Label htmlFor={restrictLibrariesId}>Restrict libraries</Label>
          <p className="text-muted-foreground text-xs">
            Limit this profile to a specific set of libraries.
          </p>
        </div>
        <Switch id={restrictLibrariesId} checked={enabled} onCheckedChange={onEnabledChange} />
      </div>

      {enabled ? (
        <div className="space-y-2">
          <div className="grid gap-2">
            {libraries.length === 0 ? (
              <p className="text-muted-foreground text-sm">No libraries available.</p>
            ) : (
              libraries.map((library) => (
                <div
                  key={library.id}
                  className="border-border flex items-center justify-between rounded-md border px-3 py-2"
                >
                  <div className="space-y-0.5">
                    <div className="text-sm font-medium">{library.name}</div>
                    <div className="text-muted-foreground text-xs capitalize">{library.type}</div>
                  </div>
                  <Switch
                    checked={allowedLibraryIDs.includes(library.id)}
                    onCheckedChange={(nextChecked) => onLibraryChange(library.id, nextChecked)}
                  />
                </div>
              ))
            )}
          </div>

          {error ? <p className="text-destructive text-sm">{error}</p> : null}
        </div>
      ) : null}
    </div>
  );
}
