import { V2TimeoutError } from "@/api/v2/request";
import { t } from "@/i18n";

/**
 * The explanation under a "Couldn't load …" heading. A read that ran out its
 * deadline says the server is not answering, which tells the viewer the
 * problem is not the page they asked for. Any other failure reads `fallback`,
 * the page's own explanation, or the shared generic one.
 */
export function loadErrorDescription(error: unknown, fallback?: string): string {
  if (error instanceof V2TimeoutError) return t("web.common.ui.loadError.timeout");
  return fallback ?? t("web.common.ui.loadError.generic");
}
