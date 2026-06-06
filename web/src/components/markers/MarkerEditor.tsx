import { useState } from "react";
import { Loader2, Trash2 } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { FileMarkersResponse, MarkerKind, SetMarkersRequest } from "@/api/types";
import { MARKER_KINDS, MARKER_LABELS, formatClock, parseClock } from "@/lib/markers";
import { useItemMarkers, useSetItemMarkers } from "@/hooks/queries/markers";

interface MarkerEditorProps {
  itemId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

type FieldState = Record<MarkerKind, { start: string; end: string }>;

function fieldsFromResponse(data: FileMarkersResponse): FieldState {
  return {
    intro: { start: formatClock(data.intro.start), end: formatClock(data.intro.end) },
    recap: { start: formatClock(data.recap.start), end: formatClock(data.recap.end) },
    credits: { start: formatClock(data.credits.start), end: formatClock(data.credits.end) },
    preview: { start: formatClock(data.preview.start), end: formatClock(data.preview.end) },
  };
}

/**
 * Curator dialog for editing a catalog item's intro/recap/credits/preview
 * markers by typing timecodes. Edits save as source="manual" via the
 * authenticated markers API.
 */
export function MarkerEditor({ itemId, open, onOpenChange }: MarkerEditorProps) {
  const { data, isLoading, isError } = useItemMarkers(itemId, { enabled: open });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Edit markers</DialogTitle>
          <DialogDescription>
            Set intro, recap, credits, and preview times. Use m:ss or h:mm:ss; leave both fields
            empty to remove a marker.
          </DialogDescription>
        </DialogHeader>

        {isLoading ? (
          <div className="text-muted-foreground flex items-center justify-center py-8">
            <Loader2 className="size-5 animate-spin" />
          </div>
        ) : isError || !data ? (
          <p className="text-destructive py-6 text-center text-sm">Failed to load markers.</p>
        ) : (
          // Keyed by file id so the form re-initializes from fresh data without
          // a state-syncing effect.
          <MarkerEditorForm
            key={data.file_id}
            itemId={itemId}
            data={data}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

function MarkerEditorForm({
  itemId,
  data,
  onClose,
}: {
  itemId: string;
  data: FileMarkersResponse;
  onClose: () => void;
}) {
  const setMarkers = useSetItemMarkers(itemId);
  const [fields, setFields] = useState<FieldState>(() => fieldsFromResponse(data));
  const [error, setError] = useState<string | null>(null);

  const setField = (kind: MarkerKind, edge: "start" | "end", value: string) => {
    setFields((prev) => ({ ...prev, [kind]: { ...prev[kind], [edge]: value } }));
    setError(null);
  };

  const clearKind = (kind: MarkerKind) => {
    setFields((prev) => ({ ...prev, [kind]: { start: "", end: "" } }));
    setError(null);
  };

  const handleSave = () => {
    const body: SetMarkersRequest = {};
    for (const kind of MARKER_KINDS) {
      const { start: startStr, end: endStr } = fields[kind];
      const original = data[kind];
      const hadValue = original.start != null || original.end != null;

      if (startStr.trim() === "" && endStr.trim() === "") {
        if (hadValue) body[kind] = null; // explicit clear
        continue;
      }

      const start = parseClock(startStr);
      const end = parseClock(endStr);
      if (start == null || end == null || Number.isNaN(start) || Number.isNaN(end)) {
        setError(`${MARKER_LABELS[kind]}: enter both start and end (e.g. 1:30).`);
        return;
      }
      if (end <= start) {
        setError(`${MARKER_LABELS[kind]}: end must be after start.`);
        return;
      }
      // Inputs carry whole-second precision; round the (possibly fractional)
      // stored values before comparing so an untouched marker isn't resaved
      // as "manual" with a rounded timestamp.
      const origStart = original.start == null ? null : Math.round(original.start);
      const origEnd = original.end == null ? null : Math.round(original.end);
      if (origStart === start && origEnd === end) continue; // unchanged
      body[kind] = { start, end };
    }

    if (Object.keys(body).length === 0) {
      onClose();
      return;
    }
    setMarkers.mutate(body, { onSuccess: () => onClose() });
  };

  return (
    <>
      <div className="grid gap-3 py-1">
        {MARKER_KINDS.map((kind) => (
          <div key={kind} className="grid grid-cols-[5.5rem_1fr_1fr_auto] items-center gap-2">
            <span className="text-sm font-medium">{MARKER_LABELS[kind]}</span>
            <Input
              aria-label={`${MARKER_LABELS[kind]} start`}
              placeholder="start"
              value={fields[kind].start}
              onChange={(e) => setField(kind, "start", e.target.value)}
            />
            <Input
              aria-label={`${MARKER_LABELS[kind]} end`}
              placeholder="end"
              value={fields[kind].end}
              onChange={(e) => setField(kind, "end", e.target.value)}
            />
            <Button
              type="button"
              variant="ghost"
              size="icon"
              aria-label={`Clear ${MARKER_LABELS[kind]}`}
              onClick={() => clearKind(kind)}
            >
              <Trash2 className="size-4" />
            </Button>
          </div>
        ))}
      </div>

      {error && <p className="text-destructive text-sm">{error}</p>}

      <DialogFooter>
        <Button variant="outline" onClick={onClose}>
          Cancel
        </Button>
        <Button onClick={handleSave} disabled={setMarkers.isPending}>
          {setMarkers.isPending && <Loader2 className="size-4 animate-spin" />}
          Save
        </Button>
      </DialogFooter>
    </>
  );
}
