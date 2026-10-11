import { useEffect, useRef } from "react";
import { AlertTriangle } from "lucide-react";

import { Button } from "@/components/ui/button";
import { CONFLICT_TITLE, joinNames } from "@/lib/collections/copy";

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
        <strong className="font-semibold">{CONFLICT_TITLE}</strong>{" "}
        {fields.length > 0
          ? `${joinNames(fields)} changed in both places.`
          : "Save again to keep your changes."}
      </p>
      <span className="flex gap-2">
        <Button type="button" size="sm" variant="outline" onClick={onUseTheirs}>
          Use theirs
        </Button>
        <Button type="button" size="sm" onClick={onKeepMine}>
          Keep mine
        </Button>
      </span>
    </div>
  );
}
