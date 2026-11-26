import { useId } from "react";

import type { UserLibrary } from "@/api/types";
import { libraryTypeLabel } from "@/components/admin/libraries/libraryTypes";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useT } from "@/i18n";
import { SWITCH_ROW_TEXT } from "./ProfileEditorDialog.model";

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
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div className={SWITCH_ROW_TEXT}>
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
          {/* grid-cols-1 holds the column to the list's width. An implicit auto
              column grows to fit a row's longest word instead. */}
          <div className="grid grid-cols-1 gap-2">
            {libraries.length === 0 ? (
              <p className="text-muted-foreground text-sm">{t("web.profiles.noLibraries")}</p>
            ) : (
              libraries.map((library) => (
                <div
                  key={library.id}
                  className="border-border flex items-center justify-between gap-3 rounded-md border px-3 py-2"
                >
                  {/* wrap-anywhere breaks a long one-word name or type instead of
                      widening the row past the list. */}
                  <div className="min-w-0 flex-1 space-y-0.5 wrap-anywhere">
                    {/* dir="auto" keeps the name's own reading order; align-with-page
                        keeps its wrapped lines on the page's start side. */}
                    <div dir="auto" className="align-with-page text-sm font-medium">
                      {library.name}
                    </div>
                    <div className="text-muted-foreground text-xs">
                      {libraryTypeLabel(library.type)}
                    </div>
                  </div>
                  <Switch
                    aria-label={library.name}
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
