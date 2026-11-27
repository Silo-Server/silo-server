import { Info } from "lucide-react";
import { useEpisodeOrder } from "@/hooks/queries/episodeOrder";
import { useT } from "@/i18n";
import { asProvider, episodeOrderLabel, fileOrderLabel } from "@/lib/episodeOrder";

/**
 * The series page note naming the show's episode order, with a link that
 * opens the dialog. Shown to admins and metadata curators.
 */
export default function EpisodeOrderSummary({
  contentId,
  onChange,
}: {
  contentId: string;
  onChange: () => void;
}) {
  const t = useT();
  const { data } = useEpisodeOrder(contentId, true, false);
  if (!data) return null;
  const { choice } = data;
  const provider = asProvider(choice.provider || data.library_provider);
  let summary = episodeOrderLabel(t, provider, choice.order_id, data.order_name);
  if (choice.layout === "absolute")
    summary = t("web.item.episodeOrder.summaryAbsolute", { order: summary });
  if (choice.layout === "custom")
    summary = t("web.item.episodeOrder.summaryCustom", { order: summary });
  if (choice.file_order && choice.file_order !== provider)
    summary = t("web.item.episodeOrder.summaryFiles", {
      order: summary,
      files: fileOrderLabel(t, asProvider(choice.file_order)),
    });
  if (data.inherited) summary = t("web.item.episodeOrder.summaryLibrary", { summary });
  return (
    <div className="text-muted-foreground flex flex-wrap items-center gap-2 text-xs">
      <Info className="size-3.5" aria-hidden="true" />
      <span>{t("web.item.episodeOrder.summary", { summary })}</span>
      <button
        type="button"
        onClick={onChange}
        className="text-primary -my-1 rounded px-1 py-1 font-medium hover:underline"
      >
        {t("web.item.episodeOrder.change")}
      </button>
    </div>
  );
}
