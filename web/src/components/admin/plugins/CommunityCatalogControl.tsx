import { useState } from "react";
import { Shield } from "lucide-react";

import type { PluginCatalogSettings } from "@/api/types";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Switch } from "@/components/ui/switch";
import { useUpdatePluginCatalogSettings } from "@/hooks/queries/admin/plugins";

export function CommunityCatalogControl({
  settings,
}: {
  settings: PluginCatalogSettings & { etag: string };
}) {
  const updateSettings = useUpdatePluginCatalogSettings();
  const [confirmDisable, setConfirmDisable] = useState(false);

  function setIncluded(include: boolean) {
    if (!include && settings.installed_community_plugin_count > 0) {
      setConfirmDisable(true);
      return;
    }
    updateSettings.mutate({ include_approved_community_plugins: include, etag: settings.etag });
  }

  function disableCommunityCatalog() {
    updateSettings.mutate({ include_approved_community_plugins: false, etag: settings.etag });
    setConfirmDisable(false);
  }

  return (
    <>
      <div className="flex items-center gap-3.5 rounded-xl border px-4 py-3.5">
        <Shield aria-hidden="true" className="text-muted-foreground size-4 shrink-0" />
        <div className="min-w-0 flex-1 space-y-0.5">
          <label htmlFor="approved-community-plugins" className="block text-sm font-semibold">
            Include approved community plugins
          </label>
          <p className="text-muted-foreground text-[13px] leading-relaxed">
            Reviewed by Silo maintainers to work as described and be safe for their documented use.
            These plugins remain maintained and supported by community contributors.
          </p>
          {settings.migrated_plugin_count > 0 ? (
            <p className="text-muted-foreground text-[13px]">
              {settings.migrated_plugin_count} existing{" "}
              {settings.migrated_plugin_count === 1 ? "installation was" : "installations were"}{" "}
              moved here without changing configuration.
            </p>
          ) : null}
        </div>
        <Switch
          id="approved-community-plugins"
          checked={settings.include_approved_community_plugins}
          disabled={updateSettings.isPending}
          onCheckedChange={setIncluded}
          aria-label="Include approved community plugins"
        />
      </div>

      <AlertDialog open={confirmDisable} onOpenChange={setConfirmDisable}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Hide approved community plugins?</AlertDialogTitle>
            <AlertDialogDescription>
              {settings.installed_community_plugin_count}{" "}
              {settings.installed_community_plugin_count === 1
                ? "installed plugin will"
                : "installed plugins will"}{" "}
              keep running, but update discovery will pause until this catalog is included again.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={disableCommunityCatalog}
              disabled={updateSettings.isPending}
            >
              Hide and pause updates
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
