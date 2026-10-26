import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";

/**
 * Raw value editor for settings with no inline control (object-valued ones
 * such as pinned sidebar items or per-library overlays). Saving replaces the
 * stored value wholesale; the caller owns the mutation.
 */
export function SettingJsonDialog({
  settingKey,
  value,
  description,
  saveLabel,
  onValueChange,
  onCancel,
  onSave,
}: {
  /** The setting key being edited; null closes the dialog. */
  settingKey: string | null;
  value: string;
  description: string;
  saveLabel: string;
  onValueChange: (value: string) => void;
  onCancel: () => void;
  onSave: () => void;
}) {
  return (
    <Dialog
      open={settingKey !== null}
      onOpenChange={(open) => {
        if (!open) onCancel();
      }}
    >
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm">{settingKey ?? "JSON"}</DialogTitle>
        </DialogHeader>
        <div className="space-y-3">
          <p className="text-muted-foreground text-[12.5px]">{description}</p>
          <textarea
            spellCheck={false}
            aria-label="Raw value"
            className="border-border bg-background focus:border-foreground/40 min-h-[260px] w-full rounded-md border px-3 py-2 font-mono text-[13px] leading-relaxed transition-colors outline-none"
            value={value}
            onChange={(event) => onValueChange(event.target.value)}
          />
          <div className="flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={onCancel}>
              Cancel
            </Button>
            <Button size="sm" onClick={onSave}>
              {saveLabel}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
