import { Button } from "@/components/ui/button";

/** What a saved list follows, as one line, with the button that changes it when it may change. */
export function SourceCard({
  title,
  detail,
  action,
  onChange,
}: {
  title: string;
  detail: string;
  action?: string;
  onChange?: () => void;
}) {
  return (
    <div className="bg-muted/40 flex flex-wrap items-center gap-3 rounded-xl px-4 py-3">
      <div className="grid min-w-0 flex-1 gap-0.5">
        <span className="text-[14px] font-semibold">{title}</span>
        <span className="text-muted-foreground truncate text-[13px]">{detail}</span>
      </div>
      {action && onChange ? (
        <Button type="button" size="sm" variant="outline" onClick={onChange}>
          {action}
        </Button>
      ) : null}
    </div>
  );
}
