import { AlertCircle } from "lucide-react";
import { useT } from "@/i18n";
import { joinList } from "@/lib/appLocale";

/**
 * Names the metadata providers whose match search failed, so an outage does
 * not read as "no candidates". Renders nothing when every provider answered.
 * It shares the image picker's "Could not load from {providers}" string,
 * whose wording fits a failed search as well.
 */
export function MatchSearchProviderErrors({ providers }: { providers: string[] }) {
  const t = useT();
  if (providers.length === 0) return null;
  return (
    <div
      role="alert"
      className="border-warning/25 bg-warning/10 text-warning-label flex items-center gap-2 rounded-lg border px-3 py-2 text-sm"
    >
      <AlertCircle className="size-4 shrink-0" />
      <span>
        {t("web.item.images.providerErrors", {
          providers: joinList(providers.map((provider) => provider.toUpperCase())),
        })}
      </span>
    </div>
  );
}
