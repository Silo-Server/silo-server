import { useMemo } from "react";
import type { FileVersion } from "@/api/types";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "@/components/ui/accordion";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { buildDetailLine, buildQualitySummary, sortByResolution } from "./VersionFlyout";
import { buildMediaSpecSections, type MediaSpecSection } from "./mediaSpecSections";

interface MediaInfoDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  versions: FileVersion[];
  title: string;
  /** Version to expand initially; falls back to the highest-quality version. */
  initialFileId?: number | null;
}

function VersionSpecSheet({ sections }: { sections: MediaSpecSection[] }) {
  return (
    <div className="space-y-4">
      {sections.map((section) => (
        <div key={section.title}>
          <div className="text-muted-foreground/70 mb-1.5 text-[11px] font-semibold tracking-wide uppercase">
            {section.title}
          </div>
          <div className="divide-border/40 bg-background/40 divide-y rounded-lg border">
            {section.rows.map((row) => (
              <div
                key={row.label}
                className="flex items-baseline justify-between gap-4 px-3 py-1.5 text-sm"
              >
                <span className="text-muted-foreground shrink-0">{row.label}</span>
                <span className="text-foreground min-w-0 text-right break-all">{row.value}</span>
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

export default function MediaInfoDialog({
  open,
  onOpenChange,
  versions,
  title,
  initialFileId,
}: MediaInfoDialogProps) {
  const sorted = useMemo(() => sortByResolution(versions), [versions]);
  const initialValue = useMemo(() => {
    const target = sorted.find((version) => version.file_id === initialFileId) ?? sorted[0];
    return target ? String(target.file_id) : undefined;
  }, [initialFileId, sorted]);
  const onlyVersion = sorted.length === 1 ? sorted[0] : undefined;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl overflow-hidden sm:max-w-2xl">
        <DialogHeader className="min-w-0">
          <DialogTitle>Media Info</DialogTitle>
          <DialogDescription className="truncate">{title}</DialogDescription>
        </DialogHeader>

        <div className="-mr-1 max-h-[65vh] min-w-0 overflow-x-hidden overflow-y-auto pr-1">
          {onlyVersion ? (
            <VersionSpecSheet sections={buildMediaSpecSections(onlyVersion)} />
          ) : (
            // DialogContent unmounts when closed, so defaultValue re-targets on each open.
            <Accordion type="multiple" defaultValue={initialValue ? [initialValue] : []}>
              {sorted.map((version, index) => {
                const summary =
                  buildQualitySummary(version) || version.file_name || `Version ${index + 1}`;
                const detail = buildDetailLine(version);

                return (
                  <AccordionItem key={version.file_id} value={String(version.file_id)}>
                    <AccordionTrigger className="py-3 hover:no-underline">
                      <span className="min-w-0 flex-1">
                        <span className="text-foreground block truncate text-sm font-medium">
                          {summary}
                        </span>
                        {detail && (
                          <span className="text-muted-foreground mt-0.5 block text-xs font-normal">
                            {detail}
                          </span>
                        )}
                      </span>
                    </AccordionTrigger>
                    <AccordionContent>
                      <VersionSpecSheet sections={buildMediaSpecSections(version)} />
                    </AccordionContent>
                  </AccordionItem>
                );
              })}
            </Accordion>
          )}
          {sorted.length === 0 && (
            <div className="text-muted-foreground rounded-xl border border-dashed px-4 py-6 text-center text-sm">
              No media files for this item.
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
