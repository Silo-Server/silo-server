import type { MediaRequest } from "@/api/types";
import { RequestDownloadProgress } from "@/components/RequestDownloadProgress";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";
import { TargetStatusBadge } from "./queueParts";
import { distinctTargets, requestOptionValue, targetServerName } from "./requestQueueModel";

const SEPARATOR = " · ";
const SPACE = " ";
const NO_VALUE = "—";

/** One cell per chosen-setting column of a queue row. */
export function OptionCells({
  request,
  columns,
}: {
  request: MediaRequest;
  columns: readonly string[];
}) {
  return columns.map((column) => {
    const value = requestOptionValue(request, column);
    return (
      // Below the wide layout there is no header row, so the cell names its
      // setting, and an empty one is left out.
      <p key={column} className={cn("min-w-0 text-xs break-words", !value && "max-lg:hidden")}>
        <span className="text-muted-foreground lg:hidden">
          {column}
          {SEPARATOR}
        </span>
        {value ?? NO_VALUE}
      </p>
    );
  });
}

export function TargetList({ request }: { request: MediaRequest }) {
  const t = useT();
  const targets = distinctTargets(request.targets);
  if (targets.length === 0) {
    return <p className="text-muted-foreground text-xs">{t("web.admin.requests.queue.notSent")}</p>;
  }
  return (
    <ul className="min-w-0 space-y-1.5 text-xs">
      {targets.map(({ target, shared }) => (
        <li key={target.id} className="min-w-0">
          <div className="flex flex-wrap items-center gap-1.5">
            {shared ? null : (
              <>
                <span className="font-medium">{target.quality}</span>
                {SPACE}
              </>
            )}
            <span>{targetServerName(target)}</span>
            {SPACE}
            <TargetStatusBadge target={target} />
          </div>
          {target.route_name ? (
            <p className="text-muted-foreground truncate">
              {t("web.admin.requests.queue.byRoute", { route: target.route_name })}
            </p>
          ) : null}
          {target.download ? (
            <RequestDownloadProgress
              download={target.download}
              admin
              className="mt-1 w-56 max-w-full"
            />
          ) : null}
          {target.last_error ? (
            <p className="text-destructive line-clamp-2 break-words">{target.last_error}</p>
          ) : null}
        </li>
      ))}
    </ul>
  );
}
