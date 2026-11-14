import { useId } from "react";

import type { UserLibrary } from "@/api/types";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useT } from "@/i18n";

/**
 * The profile editor's library access: a library limit for any profile but
 * the primary, which inherits the account's libraries and hides the ones it
 * doesn't want in Settings → Libraries instead.
 */
export function ProfileLibraryAccess({
  isPrimary,
  libraries,
  restricted,
  allowedLibraryIDs,
  error,
  onRestrictedChange,
  onLibraryChange,
}: {
  isPrimary: boolean;
  libraries: UserLibrary[];
  restricted: boolean;
  allowedLibraryIDs: number[];
  error?: string;
  onRestrictedChange: (restricted: boolean) => void;
  onLibraryChange: (libraryID: number, allowed: boolean) => void;
}) {
  const t = useT();
  const restrictLibrariesId = useId();

  if (isPrimary) {
    return (
      <div className="border-border space-y-0.5 rounded-md border p-3">
        <p className="text-sm font-medium">{t("web.settings.library.title")}</p>
        <p className="text-muted-foreground text-xs">
          {t("web.profiles.libraryAccess.primaryNote")}
        </p>
      </div>
    );
  }

  return (
    <div className="border-border space-y-3 rounded-md border p-3">
      <div className="flex items-center justify-between gap-4">
        <div className="space-y-0.5">
          <Label htmlFor={restrictLibrariesId}>{t("web.profiles.editor.restrictLibraries")}</Label>
          <p className="text-muted-foreground text-xs">
            {t("web.profiles.editor.restrictLibrariesHint")}
          </p>
        </div>
        <Switch
          id={restrictLibrariesId}
          checked={restricted}
          onCheckedChange={onRestrictedChange}
        />
      </div>

      {restricted ? (
        <div className="space-y-2">
          <div className="grid gap-2">
            {libraries.length === 0 ? (
              <p className="text-muted-foreground text-sm">{t("web.profiles.noLibraries")}</p>
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
                    onCheckedChange={(checked) => onLibraryChange(library.id, checked)}
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
