import * as React from "react";

import { cn } from "@/lib/utils";

/** A multi-line text field with the same fill, border and focus ring as `Input`. */
function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        "border-border bg-background selection:bg-primary selection:text-primary-foreground placeholder:text-muted-foreground min-h-16 w-full min-w-0 rounded-md border px-3 py-2 text-base shadow-xs transition-[color,box-shadow] outline-none disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 md:text-sm",
        "focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px]",
        "aria-invalid:border-destructive aria-invalid:ring-destructive/20",
        className,
      )}
      {...props}
    />
  );
}

export { Textarea };
