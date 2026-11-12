import { ConfirmDialog } from "@/components/ConfirmDialog";
import { useT } from "@/i18n";

/**
 * Confirms withdrawing the viewer's own request before it is sent to the
 * download automation (pending, or approved with nothing sent yet).
 */
export function CancelRequestDialog({
  title,
  open,
  onOpenChange,
  onConfirm,
  isPending,
}: {
  /** The requested title, named in the confirmation. */
  title: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
  isPending?: boolean;
}) {
  const t = useT();
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t("web.requests.cancel.title")}
      description={t("web.requests.cancel.description", { title })}
      confirmLabel={t("web.requests.cancel.action")}
      cancelLabel={t("web.requests.cancel.keep")}
      variant="destructive"
      onConfirm={onConfirm}
      isPending={isPending}
    />
  );
}
