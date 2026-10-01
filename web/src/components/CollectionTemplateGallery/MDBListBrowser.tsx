import { useState } from "react";

import type { MDBListListSummary } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useDebounce } from "@/hooks/useDebounce";
import { useMDBListSearch, useMDBListTop } from "@/hooks/queries/userCollectionImports";

interface Props {
  onPick: (list: MDBListListSummary, jsonURL: string) => void;
}

export function MDBListBrowser({ onPick }: Props) {
  const [query, setQuery] = useState("");
  const [showTop, setShowTop] = useState(false);

  // 300ms keeps typing responsive while bounding hits against the shared
  // 1000/day MDBList free-tier quota.
  const debouncedQuery = useDebounce(query, 300);
  const search = useMDBListSearch(debouncedQuery, debouncedQuery.length > 0);
  const top = useMDBListTop(showTop);

  const configured = (search.data ?? top.data)?.configured ?? null;

  if (configured === false) {
    return (
      <div className="border-border bg-muted/30 rounded-md border border-dashed px-3 py-2 text-xs">
        <p className="text-muted-foreground">
          MDBList list search isn&rsquo;t available — an admin needs to add an MDBList API key under{" "}
          <span className="font-medium">Settings → Subtitles &amp; Metadata</span>. You can still
          paste a list URL below.
        </p>
      </div>
    );
  }

  const showingResults = debouncedQuery.length > 0;
  const lists = showingResults ? search.data?.lists : top.data?.lists;
  const isLoading = showingResults ? search.isLoading : showTop && top.isLoading;
  const error = showingResults ? search.error : top.error;

  return (
    <div className="space-y-2">
      <Label htmlFor="mdblist-search">Search MDBList</Label>
      <div className="flex gap-2">
        <Input
          id="mdblist-search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="e.g. horror, oscar winners, netflix"
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => {
            setQuery("");
            setShowTop(true);
          }}
        >
          Top lists
        </Button>
      </div>

      {error ? (
        <p className="text-destructive text-xs">
          {error instanceof Error ? error.message : "Search failed"}
        </p>
      ) : null}

      {isLoading ? (
        <p className="text-muted-foreground text-xs">Searching…</p>
      ) : lists && lists.length > 0 ? (
        <ul className="border-border divide-border/60 max-h-72 divide-y overflow-y-auto rounded-md border">
          {lists.map((list) => {
            const jsonURL = list.url ? `${list.url}/json` : "";
            return (
              <li key={list.id}>
                <button
                  type="button"
                  onClick={() => onPick(list, jsonURL)}
                  className="hover:bg-muted/60 focus-visible:bg-muted flex w-full items-start justify-between gap-3 px-3 py-2 text-left transition"
                >
                  <div className="min-w-0 space-y-0.5">
                    <p className="truncate text-sm font-medium">{list.name}</p>
                    <p className="text-muted-foreground truncate text-xs">
                      by {list.user_name} · {list.mediatype === "show" ? "TV" : list.mediatype} ·{" "}
                      {list.items.toLocaleString()} item{list.items === 1 ? "" : "s"}
                      {list.likes > 0 ? ` · ♥ ${list.likes.toLocaleString()}` : ""}
                    </p>
                    {list.description ? (
                      <p className="text-muted-foreground line-clamp-2 text-xs">
                        {list.description}
                      </p>
                    ) : null}
                  </div>
                </button>
              </li>
            );
          })}
        </ul>
      ) : showingResults && search.data ? (
        <p className="text-muted-foreground text-xs">
          No public lists matched &ldquo;{debouncedQuery}&rdquo;.
        </p>
      ) : null}
    </div>
  );
}
