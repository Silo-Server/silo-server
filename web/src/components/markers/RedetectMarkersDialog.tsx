import { type ComponentType, useId } from "react";
import { ListEnd, Loader2, RefreshCw, SkipForward } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { RedetectMarkersKind } from "@/hooks/queries/items";

interface RedetectMarkersDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: (kind: RedetectMarkersKind) => void;
  isPending?: boolean;
}

const options: {
  kind: RedetectMarkersKind;
  label: string;
  description: string;
  Icon: ComponentType<{ className?: string }>;
}[] = [
  {
    kind: "intro",
    label: "Intro",
    description: "Find the opening again from chapters and the season's shared audio.",
    Icon: SkipForward,
  },
  {
    kind: "credits",
    label: "Credits",
    description: "Find the end credits again from chapters, audio, and the picture.",
    Icon: ListEnd,
  },
  {
    kind: "all",
    label: "Intro and credits",
    description: "Run both.",
    Icon: RefreshCw,
  },
];

/** Asks which markers an episode re-detection runs for. */
export default function RedetectMarkersDialog({
  open,
  onOpenChange,
  onConfirm,
  isPending = false,
}: RedetectMarkersDialogProps) {
  const id = useId();
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Re-detect Markers</DialogTitle>
          <DialogDescription>
            Run local detection again on this server. Manual markers and markers from
            higher-priority sources stay as they are.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          {options.map(({ kind, label, description, Icon }) => (
            <button
              key={kind}
              type="button"
              aria-labelledby={`${id}-${kind}-label`}
              aria-describedby={`${id}-${kind}-description`}
              disabled={isPending}
              onClick={() => onConfirm(kind)}
              className="border-border bg-surface hover:bg-surface/80 flex w-full items-start gap-3 rounded-xl border p-4 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-60"
            >
              {isPending ? (
                <Loader2 className="text-muted-foreground mt-0.5 size-5 animate-spin" />
              ) : (
                <Icon className="text-muted-foreground mt-0.5 size-5" />
              )}
              <div className="space-y-1">
                <div id={`${id}-${kind}-label`} className="text-sm font-semibold">
                  {label}
                </div>
                <div id={`${id}-${kind}-description`} className="text-muted-foreground text-sm">
                  {description}
                </div>
              </div>
            </button>
          ))}
        </div>

        <div className="flex justify-end">
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={isPending}>
            Cancel
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
