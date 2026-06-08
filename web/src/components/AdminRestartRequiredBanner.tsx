import { useState } from "react";
import { AlertTriangle, RotateCcw } from "lucide-react";
import { toast } from "sonner";

import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import {
  useAdminServerStatus,
  useRequestAdminServerRestart,
} from "@/hooks/useAdminRestartRequired";

export function AdminRestartRequiredBanner() {
  const { data: serverStatus } = useAdminServerStatus();
  const requestRestart = useRequestAdminServerRestart();
  const [showRestartConfirm, setShowRestartConfirm] = useState(false);
  const restartRequired = Boolean(serverStatus?.restart_required);
  const restartRequested = Boolean(serverStatus?.restart_requested);

  if (!restartRequired) {
    return null;
  }

  async function handleRestart() {
    try {
      await requestRestart.mutateAsync();
      toast.success("Server restart requested");
    } catch {
      toast.error("Could not restart server. Please restart manually.");
    }
    setShowRestartConfirm(false);
  }

  return (
    <>
      <section
        aria-live="polite"
        className="border-warning/25 bg-warning/10 text-warning sticky top-20 z-20 mb-5 flex flex-col gap-3 rounded-xl border px-4 py-3 text-sm backdrop-blur sm:flex-row sm:items-center sm:justify-between lg:top-5"
        role="status"
      >
        <div className="flex min-w-0 items-start gap-3">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
          <div className="min-w-0">
            <div className="font-medium text-balance">Server restart required</div>
            <div className="text-warning/80 mt-1 leading-relaxed">
              One or more server changes are waiting for a restart before they take effect.
            </div>
          </div>
        </div>
        <div className="flex shrink-0 flex-col gap-2 sm:flex-row">
          <Button
            size="sm"
            variant="outline"
            className="border-warning/35 text-warning hover:bg-warning/15 hover:text-warning"
            onClick={() => setShowRestartConfirm(true)}
            disabled={restartRequested || requestRestart.isPending}
          >
            <RotateCcw className="h-4 w-4" />
            {restartRequested || requestRestart.isPending ? "Restart Requested" : "Restart Server"}
          </Button>
        </div>
      </section>
      <ConfirmDialog
        open={showRestartConfirm}
        onOpenChange={setShowRestartConfirm}
        title="Restart server?"
        description="The server will restart to apply configuration changes. Active streams will be interrupted."
        confirmLabel="Restart"
        variant="destructive"
        onConfirm={handleRestart}
      />
    </>
  );
}
