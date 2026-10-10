import { useEffect, useMemo, useRef, useState } from "react";
import { Languages } from "lucide-react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { useMetadataAIStatus } from "@/hooks/queries/metadataAI";
import {
  isActiveTranslationJob,
  useCancelLibraryTranslation,
  useLibraryTranslationJobs,
  useTranslateLibraryMetadata,
} from "@/hooks/queries/admin/libraryTranslation";
import { catalogKeys, sectionKeys } from "@/hooks/queries/keys";
import { LANGUAGES } from "@/player/utils/languageNames";

/**
 * Prewarms a library's translations: fills in the descriptions of every item,
 * season and episode that is missing in the chosen language, so viewers do
 * not wait for on-view translation. Existing provider, manual and AI text is
 * kept. Rendered only for a saved library while AI description translation
 * is enabled.
 */
export function LibraryTranslatePanel({ libraryId }: { libraryId: number }) {
  const queryClient = useQueryClient();
  const { data: status } = useMetadataAIStatus();
  const enabled = Boolean(status?.enabled);
  const [targetLang, setTargetLang] = useState("");

  const jobsQuery = useLibraryTranslationJobs(libraryId, enabled);
  const translate = useTranslateLibraryMetadata(libraryId);
  const cancel = useCancelLibraryTranslation(libraryId);
  const jobs = useMemo(() => jobsQuery.data?.jobs ?? [], [jobsQuery.data?.jobs]);
  const activeJob = jobs.find(isActiveTranslationJob);
  const lastJob = jobs[0];

  // When the job this panel started (or saw running) finishes, report it and
  // refresh the catalog views that show the new text. The started job's id
  // comes from the enqueue answer, so a prewarm that ends before the first
  // poll is still reported.
  const watchedRef = useRef<string | null>(null);
  useEffect(() => {
    if (activeJob) {
      watchedRef.current = activeJob.id;
      return;
    }
    if (!watchedRef.current || !lastJob || lastJob.id !== watchedRef.current) return;
    watchedRef.current = null;
    if (lastJob.status === "completed") {
      toast.success(lastJob.progress_message || "Library translation finished.");
      void queryClient.invalidateQueries({ queryKey: catalogKeys.all });
      void queryClient.invalidateQueries({ queryKey: sectionKeys.all });
    } else if (lastJob.status === "failed") {
      toast.error(lastJob.error_message || "Library translation failed.");
    }
  }, [activeJob, lastJob, queryClient]);

  if (!enabled) return null;

  const busy = translate.isPending || Boolean(activeJob);
  const start = () => {
    if (!targetLang) {
      toast.error("Pick a target language first.");
      return;
    }
    translate.mutate(targetLang, {
      onSuccess: (job) => {
        watchedRef.current = job.id;
      },
    });
  };

  return (
    <div className="border-border bg-muted/30 space-y-3 rounded-md border px-3 py-3">
      <div className="flex items-center gap-2">
        <Languages className="text-muted-foreground h-4 w-4" aria-hidden="true" />
        <span className="text-sm font-medium">Translate library descriptions</span>
      </div>
      <p className="text-muted-foreground text-xs">
        Translates every overview and tagline in this library, with season and episode overviews,
        that is missing in the chosen language. Descriptions that already exist in that language are
        kept. Viewers otherwise get translations as they browse.
      </p>
      <div className="flex flex-wrap items-end gap-3">
        <div className="space-y-1">
          <Label htmlFor={`library-translate-target-${libraryId}`} className="text-xs">
            Language
          </Label>
          <select
            id={`library-translate-target-${libraryId}`}
            className="border-border bg-background text-foreground h-9 rounded-md border px-2 text-sm"
            value={targetLang}
            onChange={(event) => setTargetLang(event.target.value)}
            disabled={busy}
          >
            <option value="">Select…</option>
            {LANGUAGES.map((lang) => (
              <option key={lang.code} value={lang.code}>
                {lang.label}
              </option>
            ))}
          </select>
        </div>
        {activeJob ? (
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => cancel.mutate(activeJob.id)}
            disabled={cancel.isPending}
          >
            Stop
          </Button>
        ) : (
          <Button type="button" size="sm" onClick={start} disabled={busy}>
            {translate.isPending ? "Starting…" : "Translate"}
          </Button>
        )}
      </div>
      {activeJob && (
        <p className="text-muted-foreground text-xs" role="status">
          {activeJob.progress_message || "Queued"}
          {activeJob.fields_total > 0 && ` (${Math.round(activeJob.progress * 100)}%)`}
        </p>
      )}
    </div>
  );
}
