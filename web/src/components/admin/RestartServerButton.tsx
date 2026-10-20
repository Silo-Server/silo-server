import { useState, type ComponentProps } from "react";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { useRequestServerRestart } from "@/hooks/queries/admin/serverRestart";
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
  const restart = useRequestServerRestart();

  async function handleRestart() {
    setShowConfirm(false);
    try {
      const result = await restart.mutateAsync();
      toast.success(
        result.status === "already_requested"
          ? "Server restart is already in progress."
          : "Silo is shutting down to restart.",
      );
    } catch {
      toast.error("Could not confirm the restart. Check server status or restart manually.");
    }
  }

  return (
    <>
      <Button
        variant={variant}
        size={size}
        className={className}
        onClick={() => setShowConfirm(true)}
        disabled={restart.isPending}
      >
        <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
        {label}
      </Button>
      <ConfirmDialog
        open={showConfirm}
        onOpenChange={setShowConfirm}
        title="Restart server?"
        description="Silo will shut down. It comes back on its own only if a service manager such as Docker or systemd restarts it; otherwise, start it again yourself. Active streams will be interrupted."
        confirmLabel="Restart"
        variant="destructive"
        onConfirm={handleRestart}
      />
    </>
  );
}
