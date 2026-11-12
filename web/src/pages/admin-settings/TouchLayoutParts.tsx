import type { ReactNode } from "react";
import { ArrowRight, XIcon } from "lucide-react";
import { Link } from "react-router";

import { Button } from "@/components/ui/button";
import { DialogClose, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useT } from "@/i18n";

/**
 * A link to another settings page inside a sentence. Below `lg` an invisible
 * box grows its tap area to about 44px tall without changing the line height.
 */
export function SettingsLink({ to, children }: { to: string; children: ReactNode }) {
  return (
    <Link
      to={to}
      className="text-foreground inline-flex items-center gap-1 font-medium hover:underline max-lg:relative max-lg:after:absolute max-lg:after:inset-x-0 max-lg:after:-inset-y-3"
    >
      {children}
      <ArrowRight className="h-3 w-3" aria-hidden="true" />
    </Link>
  );
}

/**
 * A dialog header with a 44px close button of its own below `sm`, for dialogs
 * using `FULL_SCREEN_DIALOG_CLASS`. From `sm` up it matches a plain header.
 */
export function FullScreenDialogHeader({
  title,
  description,
}: {
  title: ReactNode;
  description: ReactNode;
}) {
  const t = useT();
  return (
    <DialogHeader className="max-sm:flex-row max-sm:items-start max-sm:text-start">
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <DialogTitle className="max-sm:pt-3">{title}</DialogTitle>
        <DialogDescription>{description}</DialogDescription>
      </div>
      <DialogClose asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="text-muted-foreground -me-2 size-11 shrink-0 sm:hidden"
        >
          <XIcon className="size-5" aria-hidden="true" />
          <span className="sr-only">{t("web.ui.close")}</span>
        </Button>
      </DialogClose>
    </DialogHeader>
  );
}
