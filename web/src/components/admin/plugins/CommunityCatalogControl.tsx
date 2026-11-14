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
import { useT } from "@/i18n";

export function CommunityCatalogControl({
  settings,
}: {
  settings: PluginCatalogSettings & { etag: string };
}) {
  const t = useT();
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
            {t("web.admin.plugins.communityCatalog.includeLabel")}
          </label>
          <p className="text-muted-foreground text-[13px] leading-relaxed">
            {t("web.admin.plugins.communityCatalog.description")}
          </p>
          {settings.migrated_plugin_count > 0 ? (
            <p className="text-muted-foreground text-[13px]">
              {t("web.admin.plugins.communityCatalog.migrated", {
                count: settings.migrated_plugin_count,
                count_text: settings.migrated_plugin_count,
              })}
            </p>
          ) : null}
        </div>
        <Switch
          id="approved-community-plugins"
          checked={settings.include_approved_community_plugins}
          disabled={updateSettings.isPending}
          onCheckedChange={setIncluded}
          aria-label={t("web.admin.plugins.communityCatalog.includeLabel")}
        />
      </div>

      <AlertDialog open={confirmDisable} onOpenChange={setConfirmDisable}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("web.admin.plugins.communityCatalog.hideTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("web.admin.plugins.communityCatalog.hideDescription", {
                count: settings.installed_community_plugin_count,
                count_text: settings.installed_community_plugin_count,
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={disableCommunityCatalog}
              disabled={updateSettings.isPending}
            >
              {t("web.admin.plugins.communityCatalog.hideConfirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
