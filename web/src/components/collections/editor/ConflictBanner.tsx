import { useEffect, useRef } from "react";
import { AlertTriangle } from "lucide-react";

import { Button } from "@/components/ui/button";
import { conflictTitle, joinNames } from "@/lib/collections/copy";
import { useT } from "@/i18n";

/**
 * Shown when a save found fields someone else changed too. It takes focus,
 * names the fields, and lets the person keep theirs or take the saved ones.
 */
export function ConflictBanner({
  fields,
  onKeepMine,
  onUseTheirs,
}: {
  fields: readonly string[];
  onKeepMine: () => void;
  onUseTheirs: () => void;
}) {
  const t = useT();
  const banner = useRef<HTMLDivElement>(null);
  useEffect(() => banner.current?.focus(), []);
  return (
    <div
      ref={banner}
      role="alert"
      tabIndex={-1}
      className="border-warning/50 bg-warning/10 flex flex-wrap items-center gap-3 rounded-2xl border px-4 py-3 text-[13.5px] outline-none"
    >
      <AlertTriangle aria-hidden className="text-warning size-4 shrink-0" />
      <p className="min-w-0 flex-1">
        {t("web.collections.editor.conflictLine", {
          title: conflictTitle(),
          detail:
            fields.length > 0
              ? t("web.collections.editor.conflictFields", { fields: joinNames(fields) })
              : t("web.collections.editor.conflictSaveAgain"),
          strong: (chunks) => <strong className="font-semibold">{chunks}</strong>,
        })}
      </p>
      <span className="flex gap-2">
        <Button type="button" size="sm" variant="outline" onClick={onUseTheirs}>
          {t("web.collections.editor.useTheirs")}
        </Button>
        <Button type="button" size="sm" onClick={onKeepMine}>
          {t("web.collections.editor.keepMine")}
        </Button>
      </span>
    </div>
  );
}
