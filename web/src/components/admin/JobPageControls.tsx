import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";

export function JobPageControls({
  query,
  label,
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
  const t = useT();
  return (
    <div className="flex flex-wrap gap-2 p-3">
      {query.isError && (
        <div role="alert">
          {t("web.admin.common.jobPageControls.loadFailed", {
            button: (chunks) => (
              <Button variant="outline" onClick={() => void query.restart()}>
                {chunks}
              </Button>
            ),
          })}
        </div>
      )}
      {query.hasNextPage && (
        <Button
          variant="outline"
          disabled={query.isFetchingNextPage}
          onClick={() => void query.fetchNextPage()}
        >
          {label ?? t("web.admin.common.jobPageControls.loadOlder")}
        </Button>
      )}
    </div>
  );
}
