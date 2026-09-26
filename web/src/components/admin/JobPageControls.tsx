import { Button } from "@/components/ui/button";

export function JobPageControls({
  query,
  label = "Load older jobs",
}: {
  query: {
    isError: boolean;
    hasNextPage: boolean;
    isFetchingNextPage: boolean;
    fetchNextPage: () => Promise<unknown>;
    restart: () => Promise<unknown>;
  };
  label?: string;
}) {
  return (
    <div className="flex flex-wrap gap-2 p-3">
      {query.isError && (
        <div role="alert">
          Jobs could not load.{" "}
          <Button variant="outline" onClick={() => void query.restart()}>
            Restart jobs
          </Button>
        </div>
      )}
      {query.hasNextPage && (
        <Button
          variant="outline"
          disabled={query.isFetchingNextPage}
          onClick={() => void query.fetchNextPage()}
        >
          {label}
        </Button>
      )}
    </div>
  );
}
