import { ConfirmDialog } from "@/components/ConfirmDialog";

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
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Cancel this request?"
      description={`Your request for "${title}" will be withdrawn before it is sent to the download automation.`}
      confirmLabel="Cancel request"
      cancelLabel="Keep request"
      variant="destructive"
      onConfirm={onConfirm}
      isPending={isPending}
    />
  );
}
