import { useState, type ComponentProps } from "react";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
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
  label,
  variant = "outline",
  size = "sm",
  className,
}: RestartServerButtonProps = {}) {
  const t = useT();
  const [showConfirm, setShowConfirm] = useState(false);
  const restart = useRequestServerRestart();

  async function handleRestart() {
    setShowConfirm(false);
    try {
      const result = await restart.mutateAsync();
      toast.success(
        result.status === "already_requested"
          ? t("web.admin.common.restartServer.alreadyRequested")
          : t("web.admin.common.restartServer.shuttingDown"),
      );
    } catch {
      toast.error(t("web.admin.common.restartServer.failed"));
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
        <RotateCcw className="me-1.5 h-3.5 w-3.5" />
        {label ?? t("web.admin.common.restartServer.button")}
      </Button>
      <ConfirmDialog
        open={showConfirm}
        onOpenChange={setShowConfirm}
        title={t("web.admin.common.restartServer.confirmTitle")}
        description={t("web.admin.common.restartServer.confirmDescription")}
        confirmLabel={t("web.admin.common.restartServer.confirm")}
        variant="destructive"
        onConfirm={handleRestart}
      />
    </>
  );
}
