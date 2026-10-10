import { useState } from "react";
import { Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

export const CHECKBOX = "accent-primary size-3.5 cursor-pointer disabled:cursor-default";

/** A search box that applies its text on Enter or when it loses focus. */
export function SearchField({
  label,
  className,
  onSearch,
}: {
  label: string;
  className?: string;
  onSearch: (query: string) => void;
}) {
  const [text, setText] = useState("");
  return (
    <form
      className="relative"
      onSubmit={(event) => {
        event.preventDefault();
        onSearch(text.trim());
      }}
    >
      <Search
        className="text-muted-foreground absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2"
        aria-hidden="true"
      />
      <Input
        value={text}
        onChange={(event) => setText(event.target.value)}
        onBlur={() => onSearch(text.trim())}
        placeholder={label}
        aria-label={label}
        className={cn("h-8 pl-8 text-sm", className)}
      />
    </form>
  );
}

/** The next-page button under an infinite list; renders nothing on the last page. */
export function LoadMoreButton({
  query,
}: {
  query: { hasNextPage: boolean; isFetchingNextPage: boolean; fetchNextPage: () => unknown };
}) {
  if (!query.hasNextPage) return null;
  return (
    <div className="flex justify-center">
      <Button
        variant="outline"
        size="sm"
        disabled={query.isFetchingNextPage}
        onClick={() => void query.fetchNextPage()}
      >
        {query.isFetchingNextPage ? "Loading…" : "Load more"}
      </Button>
    </div>
  );
}

/** A tab label's count badge, highlighted while there is something to count. */
export function TabCount({ count }: { count: number }) {
  return (
    <span
      className={`rounded-md px-1.5 py-0.5 text-[10px] leading-none font-bold tabular-nums ${
        count > 0 ? "bg-primary/10 text-primary" : "bg-surface text-muted-foreground"
      }`}
    >
      {count > 99 ? "99+" : count}
    </span>
  );
}
