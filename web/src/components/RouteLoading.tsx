import { useT } from "@/i18n";

/** What a lazy route, and the admin layout while its text loads, show instead of a blank page. */
export function RouteLoading() {
  const t = useT();
  return (
    <div className="p-8" role="status" aria-live="polite">
      <span className="sr-only">{t("web.common.shell.loadingPage")}</span>
      {t("web.ui.loading")}
    </div>
  );
}
