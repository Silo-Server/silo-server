import { useState } from "react";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { api } from "@/api/client";
import { RotateCcw } from "lucide-react";
import { toast } from "sonner";

export function RestartServerButton() {
  const [showConfirm, setShowConfirm] = useState(false);

  async function handleRestart() {
    try {
      await api("/admin/server/restart", { method: "POST" });
      toast.success("Server is restarting...");
    } catch {
      toast.error("Could not restart server. Please restart manually.");
    }
    setShowConfirm(false);
  }

  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setShowConfirm(true)}>
        <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
        Restart Server
      </Button>
      <ConfirmDialog
        open={showConfirm}
        onOpenChange={setShowConfirm}
        title="Restart server?"
        description="The server will restart to apply configuration changes. Active streams will be interrupted."
        confirmLabel="Restart"
        variant="destructive"
        onConfirm={handleRestart}
      />
    </>
  );
}
