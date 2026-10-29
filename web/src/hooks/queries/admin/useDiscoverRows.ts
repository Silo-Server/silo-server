/**
 * Requests > Discover Page's rows, through the shared rows page. Every change
 * writes the whole layout over the version it was made against, one write at
 * a time, so a change saves as soon as it is made, as on Home rows. A 412
 * shows the page's conflict banner.
 */
import { useCallback, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  isRequestEditorConflict,
  putAdminDiscoverLayoutV2,
  requestValidationErrors,
  resetAdminDiscoverLayoutV2,
  type DiscoverLayout,
  type DiscoverRow,
  type DiscoverRowBody,
} from "@/api/v2/adminRequests";
import { HOME_PAGE } from "@/lib/homeRows/pages";
import { stableJson } from "@/lib/homeRows/stableJson";
import {
  RowChangedError,
  type HomeRowsConflict,
  type HomeRowsListAdapter,
} from "@/lib/homeRows/types";
import {
  layoutBodies,
  rowBody,
  rowErrors,
  toHomeRow,
  withRow,
} from "@/pages/admin-requests/discoverLayoutModel";
import { adminKeys, requestKeys } from "../keys";
import { useRequestDiscoverLayout } from "./requestDiscover";

/** A change to the rows, made to whatever version is newest when its write runs. */
type RowsChange = <Row extends DiscoverRowBody>(rows: readonly Row[]) => Row[];

/** Thrown by `saveRow` when the server refuses the row; `problems` say why, in words. */
export class RowRefusedError extends Error {
  constructor(readonly problems: string[]) {
    super(problems.join(" "));
    this.name = "RowRefusedError";
  }
}

export interface DiscoverRowsAdapter extends HomeRowsListAdapter {
  layout: DiscoverLayout | undefined;
  /** The stored rows as the list shows them, with their lists' status. */
  discoverRows: DiscoverRow[];
  /**
   * Saves a new or changed row over the newest layout and resolves with its
   * id. `base` is the stored row an edit started from: when the newest layout
   * holds anything else under its id, the save rejects with RowChangedError
   * rather than write over another admin's change. Rejects on a 412
   * (isRequestEditorConflict) or with RowRefusedError too.
   */
  saveRow(row: DiscoverRowBody, base?: DiscoverRowBody | null): Promise<string | undefined>;
  removeRow(id: string): Promise<void>;
  /** Drops the stored layout so the default rows apply; resolves false when it didn't. */
  restoreDefaults(): Promise<boolean>;
}

function message(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}

/** The one page Discover has; the shared page names it by its label. */
const PAGES = [{ ref: HOME_PAGE, label: "Discover" }];

export function useDiscoverRows(): DiscoverRowsAdapter {
  const queryClient = useQueryClient();
  const layoutQuery = useRequestDiscoverLayout();
  const layout = layoutQuery.data;
  // The rows as the writes still in flight leave them.
  const [onScreen, setOnScreen] = useState<DiscoverRow[] | null>(null);
  const [conflict, setConflict] = useState<HomeRowsConflict>(null);
  const [pendingCount, setPendingCount] = useState(0);
  const inFlight = useRef(0);
  const queue = useRef<Promise<unknown>>(Promise.resolve());

  const run = useCallback(
    <T>(job: (latest: DiscoverLayout) => Promise<T>): Promise<T> => {
      inFlight.current++;
      setPendingCount((count) => count + 1);
      const result = queue.current
        .then(() => {
          const latest = queryClient.getQueryData<DiscoverLayout>(
            adminKeys.requestDiscoverLayout(),
          );
          if (!latest) throw new Error("The Discover page hasn't loaded.");
          return job(latest);
        })
        .finally(() => {
          setPendingCount((count) => count - 1);
          if (--inFlight.current === 0) setOnScreen(null);
        });
      queue.current = result.catch(() => undefined);
      return result;
    },
    [queryClient],
  );

  const put = useCallback(
    async (rows: DiscoverRowBody[], etag: string) => {
      // A read still in flight (the list-status poll) must not land over the save.
      await queryClient.cancelQueries({ queryKey: adminKeys.requestDiscoverLayout() });
      const saved = await putAdminDiscoverLayoutV2(rows, { etag });
      queryClient.setQueryData(adminKeys.requestDiscoverLayout(), saved);
      // Everyone's Discover page reads the layout.
      void queryClient.invalidateQueries({ queryKey: requestKeys.discovery() });
      return saved;
    },
    [queryClient],
  );

  /** A change made from the row list: shown at once, reported here when it fails. */
  function listWrite(change: RowsChange, failure: string, etag?: string) {
    setOnScreen((current) => change(current ?? layout?.rows ?? []));
    return run((latest) => put(change(layoutBodies(latest)), etag ?? latest.etag)).then(
      () => undefined,
      (error: unknown) => {
        if (isRequestEditorConflict(error)) setConflict({ scope: "page" });
        else toast.error(message(error, failure));
        void layoutQuery.refetch();
      },
    );
  }

  const discoverRows = useMemo(() => onScreen ?? layout?.rows ?? [], [onScreen, layout?.rows]);
  const rows = useMemo(() => discoverRows.map(toHomeRow), [discoverRows]);
  const pending = pendingCount > 0;
  let status: HomeRowsListAdapter["status"] = "ready";
  if (layoutQuery.isError && !layout) status = "error";
  else if (!layout) status = "loading";

  return {
    surface: "admin",
    page: HOME_PAGE,
    pages: PAGES,
    setPage: () => undefined,
    status,
    error: layoutQuery.error ? message(layoutQuery.error, "Could not load the rows.") : null,
    canEdit: status === "ready",
    rows,
    discoverRows,
    layout,
    pending,
    conflict,
    reload: async () => {
      setConflict(null);
      await layoutQuery.refetch();
    },
    canReorder: status === "ready" && !pending && conflict === null,
    orderToken: layout?.etag,
    reorder: (orderedIds, orderToken) =>
      listWrite(
        (current) =>
          [...current].sort(
            (a, b) => orderedIds.indexOf(a.id ?? "") - orderedIds.indexOf(b.id ?? ""),
          ),
        "Could not move this row",
        typeof orderToken === "string" ? orderToken : undefined,
      ),
    setShown: (id, shown) =>
      listWrite(
        (current) => current.map((row) => (row.id === id ? { ...row, enabled: shown } : row)),
        "Could not save this row",
      ),
    removeRow: (id) =>
      listWrite((current) => current.filter((row) => row.id !== id), "Could not delete this row"),
    saveRow: (row, base) =>
      run(async (latest) => {
        if (base) {
          const stored = latest.rows.find((entry) => entry.id === base.id);
          if (!stored || stableJson(rowBody(stored)) !== stableJson(base)) {
            throw new RowChangedError();
          }
        }
        const before = new Set(latest.rows.map((entry) => entry.id));
        const rows = withRow(layoutBodies(latest), row);
        try {
          const saved = await put(rows, latest.etag);
          return row.id ?? saved.rows.find((entry) => !before.has(entry.id))?.id;
        } catch (error) {
          const validation = requestValidationErrors(error);
          if (!validation) throw error;
          const index = row.id ? rows.findIndex((entry) => entry.id === row.id) : rows.length - 1;
          const problems = rowErrors(validation.fields, index);
          throw new RowRefusedError(
            problems.length > 0 ? problems : [validation.fields.rows ?? validation.message],
          );
        }
      }),
    restoreDefaults: () =>
      run(async (latest) => {
        await resetAdminDiscoverLayoutV2(latest);
        await queryClient.invalidateQueries({ queryKey: adminKeys.requestDiscoverLayout() });
        void queryClient.invalidateQueries({ queryKey: requestKeys.discovery() });
      }).then(
        () => true,
        (error: unknown) => {
          if (isRequestEditorConflict(error)) setConflict({ scope: "page" });
          else toast.error(message(error, "Could not restore the default rows"));
          return false;
        },
      ),
  };
}
