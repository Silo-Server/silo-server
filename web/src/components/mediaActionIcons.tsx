import { History, Pencil, RefreshCw, Search } from "lucide-react";
import { cn } from "@/lib/utils";

export type SharedMediaActionIconKey =
  | "viewPlayHistory"
  | "refreshMetadata"
  | "editMetadata"
  | "matchItem";

export function MediaActionIcon({
  action,
  isPending = false,
}: {
  action: SharedMediaActionIconKey;
  isPending?: boolean;
}) {
  switch (action) {
    case "viewPlayHistory":
      return <History aria-hidden="true" className="size-4" />;
    case "refreshMetadata":
      return <RefreshCw aria-hidden="true" className={cn("size-4", isPending && "animate-spin")} />;
    case "editMetadata":
      return <Pencil aria-hidden="true" className="size-4" />;
    case "matchItem":
      return <Search aria-hidden="true" className="size-4" />;
  }
}
