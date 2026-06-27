import { useEffect, useMemo, useRef, useState } from "react";
import { Languages } from "lucide-react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import type { ItemDetail } from "@/api/types";
import {
  useMetadataAIStatus,
  useMetadataTranslationJobs,
  useTranslateItemMetadata,
  type MetadataTranslationJob,
} from "@/hooks/queries/items";
import { invalidateMediaSurfaceQueries } from "@/hooks/queries/mediaSurfaceRefresh";
import { LANGUAGES } from "@/player/utils/languageNames";

function isActive(job: MetadataTranslationJob): boolean {
  return job.status === "pending" || job.status === "running";
}

/**
 * "Translate with AI" block for the metadata editor: translates the item's
 * overview/tagline (and, for series, all season/episode overviews) into a
 * chosen language. Results land in the localization tables marked as
 * AI-sourced, so a later provider refresh can replace them but never the
 * other way around. Rendered only when the server has metadata AI enabled.
 */
export function MetadataTranslatePanel({ item }: { item: ItemDetail }) {
  const queryClient = useQueryClient();
  const { data: status } = useMetadataAIStatus();
  const enabled = Boolean(status?.enabled);

  const [targetLang, setTargetLang] = useState("");
  const [force, setForce] = useState(false);
  const [watching, setWatching] = useState(false);

  const translateMutation = useTranslateItemMetadata(item.content_id);
  const { data: jobsData } = useMetadataTranslationJobs(item.content_id, enabled && watching);

  const activeJob = useMemo(() => (jobsData?.jobs ?? []).find(isActive), [jobsData]);
  const lastJob = jobsData?.jobs?.[0];

  // When the job we were watching reaches a terminal state, surface the
  // outcome and refresh the (now possibly re-localized) detail surfaces.
  const sawActiveRef = useRef(false);
  useEffect(() => {
    if (activeJob) {
      sawActiveRef.current = true;
      return;
    }
    if (!sawActiveRef.current || !lastJob) return;
    sawActiveRef.current = false;
    setWatching(false);
    if (lastJob.status === "completed") {
      toast.success(
        lastJob.fields_total === 0
          ? "Nothing to translate — all descriptions are already localized."
          : `Translated ${lastJob.fields_done} description${lastJob.fields_done === 1 ? "" : "s"}.`,
      );
      void invalidateMediaSurfaceQueries(queryClient, { itemId: item.content_id });
    } else if (lastJob.status === "failed") {
      toast.error(lastJob.error_message || "Translation failed.");
    }
  }, [activeJob, lastJob, queryClient, item.content_id]);

  if (!enabled) return null;

  const busy = translateMutation.isPending || Boolean(activeJob);
  const translatesChildren = item.type === "series";
  const description =
    item.type === "series"
      ? "Translates the overview and tagline plus all season and episode overviews"
      : item.type === "movie"
        ? "Translates the overview and tagline"
        : "Translates the overview";

  function start() {
    if (!targetLang) {
      toast.error("Pick a target language first.");
      return;
    }
    translateMutation.mutate(
      { target_language: targetLang, include_children: translatesChildren, force },
      { onSuccess: () => setWatching(true) },
    );
  }

  return (
    <div className="border-border bg-muted/30 space-y-3 rounded-md border px-3 py-3">
      <div className="flex items-center gap-2">
        <Languages className="text-muted-foreground h-4 w-4" />
        <span className="text-sm font-medium">Translate with AI</span>
      </div>
      <p className="text-muted-foreground text-xs">
        {description} into the chosen language. Translations are served to libraries using that
        metadata language; provider data replaces them when it becomes available.
      </p>
      <div className="flex flex-wrap items-end gap-3">
        <div className="space-y-1">
          <Label htmlFor="translate-target" className="text-xs">
            Language
          </Label>
          <select
            id="translate-target"
            className="border-border bg-background text-foreground h-9 rounded-md border px-2 text-sm"
            value={targetLang}
            onChange={(e) => setTargetLang(e.target.value)}
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
        <div className="flex h-9 items-center gap-2">
          <Switch id="translate-force" checked={force} onCheckedChange={setForce} disabled={busy} />
          <Label htmlFor="translate-force" className="text-xs">
            Re-translate existing
          </Label>
        </div>
        <Button type="button" size="sm" onClick={start} disabled={busy}>
          {busy ? "Translating…" : "Translate"}
        </Button>
      </div>
      {activeJob && (
        <p className="text-muted-foreground text-xs">
          {activeJob.progress_message || "Working"}…{" "}
          {activeJob.fields_total > 0 &&
            `${activeJob.fields_done}/${activeJob.fields_total} fields`}
        </p>
      )}
    </div>
  );
}
