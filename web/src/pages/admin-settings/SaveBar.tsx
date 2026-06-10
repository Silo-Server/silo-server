import { Button } from "@/components/ui/button";
import { AlertTriangle } from "lucide-react";
import { RestartServerButton } from "./RestartServerButton";

interface SaveBarProps {
  dirtyCount: number;
  onSave: () => void;
  onDiscard: () => void;
  isSaving: boolean;
  restartRequired: boolean;
}

export function SaveBar({
  dirtyCount,
  onSave,
  onDiscard,
  isSaving,
  restartRequired,
}: SaveBarProps) {
  return (
    <div className="surface-panel-subtle mt-6 rounded-xl p-4">
      {restartRequired && (
        <div className="text-foreground/80 mb-3 flex items-center gap-2 text-xs">
          <AlertTriangle className="h-3.5 w-3.5" />
          <span>Server restart required for changes to take effect.</span>
        </div>
      )}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <span className="text-muted-foreground text-sm">
          {dirtyCount > 0 ? `${dirtyCount} unsaved change${dirtyCount > 1 ? "s" : ""}` : ""}
        </span>
        <div className="flex flex-col gap-2 sm:flex-row">
          {restartRequired && <RestartServerButton />}
          <Button variant="outline" size="sm" onClick={onDiscard} disabled={dirtyCount === 0}>
            Discard
          </Button>
          <Button size="sm" onClick={onSave} disabled={dirtyCount === 0 || isSaving}>
            {isSaving ? "Saving..." : "Save Changes"}
          </Button>
        </div>
      </div>
    </div>
  );
}
