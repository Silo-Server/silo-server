import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";

interface ReportProgressVars {
  contentId: string;
  positionSeconds: number;
  mediaFileId?: number;
}

interface ReportProgressBody {
  position_seconds: number;
  media_file_id?: number;
}

export function useReportAudiobookProgress() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ contentId, positionSeconds, mediaFileId }: ReportProgressVars) => {
      const body: ReportProgressBody = { position_seconds: positionSeconds };
      if (mediaFileId !== undefined) {
        body.media_file_id = mediaFileId;
      }
      return api(`/audiobooks/${contentId}/progress`, {
        method: "POST",
        body: JSON.stringify(body),
      });
    },
    onSuccess: (_, vars) => {
      qc.invalidateQueries({ queryKey: ["audiobooks", "detail", vars.contentId] });
    },
  });
}
