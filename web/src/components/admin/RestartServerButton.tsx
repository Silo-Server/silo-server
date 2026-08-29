import { useState, type ComponentProps } from "react";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { api } from "@/api/client";
import { RotateCcw } from "lucide-react";
import { toast } from "sonner";

export interface RestartServerButtonProps {
  label?: string;
  variant?: ComponentProps<typeof Button>["variant"];
  size?: ComponentProps<typeof Button>["size"];
  className?: string;
}

export function RestartServerButton({
  label = "Restart Server",
  variant = "outline",
  size = "sm",
  className,
}: RestartServerButtonProps = {}) {
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
      <Button
        variant={variant}
        size={size}
        className={className}
        onClick={() => setShowConfirm(true)}
      >
        <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
        {label}
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
