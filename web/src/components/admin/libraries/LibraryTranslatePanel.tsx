import { useEffect, useMemo, useState } from "react";
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
  watchedLibraryTranslationJobs,
} from "@/hooks/queries/admin/libraryTranslation";
import { refreshLocalizedMediaQueries } from "@/hooks/queries/mediaSurfaceRefresh";
import { LANGUAGES } from "@/player/utils/languageNames";

function languageLabel(code: string): string {
  return LANGUAGES.find((lang) => lang.code === code)?.label ?? code;
}

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
  const activeJobs = jobs.filter(isActiveTranslationJob);
  // With one prewarm running, Stop replaces Translate; with several (two
  // languages started from different tabs), each status row gets its own.
  const activeJob = activeJobs.length === 1 ? activeJobs[0] : undefined;

  // When a watched job finishes, report it and refresh the catalog views that
  // show the new text; a failed or stopped job has still saved the items it
  // translated. The enqueue hook adds the started job before the list is
  // refetched, so a prewarm that ends before the first poll is still reported,
  // and the set outlives the panel, so a job that finishes while the editor is
  // closed is reported when the panel next sees it.
  const watched = watchedLibraryTranslationJobs(queryClient);
  useEffect(() => {
    let finished = false;
    for (const job of jobs) {
      if (isActiveTranslationJob(job)) {
        watched.add(job.id);
        continue;
      }
      if (!watched.delete(job.id)) continue;
      finished = true;
      if (job.status === "completed") {
        toast.success(job.progress_message || "Library translation finished.");
      } else if (job.status === "failed") {
        toast.error(job.error_message || "Library translation failed.");
      }
    }
    if (finished) {
      void refreshLocalizedMediaQueries(queryClient);
    }
  }, [jobs, watched, queryClient]);

  if (!enabled) return null;

  const busy = translate.isPending || activeJobs.length > 0;
  const start = () => {
    if (!targetLang) {
      toast.error("Pick a target language first.");
      return;
    }
    translate.mutate(targetLang);
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
      {activeJobs.map((job) => (
        <div key={job.id} className="flex items-center gap-2">
          <p className="text-muted-foreground text-xs" role="status">
            {activeJobs.length > 1 && `${languageLabel(job.target_language)}: `}
            {job.progress_message || "Queued"}
            {job.fields_total > 0 && ` (${Math.round(job.progress * 100)}%)`}
          </p>
          {!activeJob && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => cancel.mutate(job.id)}
              disabled={cancel.isPending}
            >
              Stop
            </Button>
          )}
        </div>
      ))}
    </div>
  );
}
