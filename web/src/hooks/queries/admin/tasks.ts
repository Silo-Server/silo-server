import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { v2, V2ProblemError } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";
import type { TriggerConfig } from "@/api/types";
import { adminKeys } from "@/hooks/queries/keys";
import { usePageActivity } from "@/hooks/usePageActivity";

export type MetadataRefreshMetrics = components["schemas"]["AdminTaskMetrics"];
export type TaskSchedule = components["schemas"]["AdminTaskSchedule"] & { etag: string };
export async function fetchTaskSchedule(key: string): Promise<TaskSchedule> {
  let etag = "";
  const schedule = await v2("GET /api/v2/admin/tasks/{key}/triggers", {
    path: { key },
    onResponse: (response) => {
      etag = response.headers.get("ETag") ?? "";
    },
  });
  if (!etag || etag === "*" || etag.startsWith("W/"))
    throw new Error("Schedule revision unavailable. Reload before editing.");
  return { ...schedule, etag };
}
export function taskMutationMessage(error: unknown) {
  if (error instanceof V2ProblemError && error.status === 412)
    return "The schedule changed. Your draft is kept. Review the current schedule before submitting a new edit.";
  if (error instanceof V2ProblemError) return error.message;
  return "The outcome is unknown. Check current task state before trying again.";
}
export function useTasks() {
  return useQuery({
    queryKey: adminKeys.tasks(),
    queryFn: async () => (await v2("GET /api/v2/admin/tasks")).items,
    staleTime: 0,
  });
}
// Hidden tasks are internal workers the Scheduled Tasks page omits. Activity
// views still need them to show what is running right now.
export function useTasksIncludingHidden() {
  return useQuery({
    queryKey: adminKeys.tasksIncludingHidden(),
    queryFn: async () =>
      (await v2("GET /api/v2/admin/tasks", { query: { include_hidden: true } })).items,
    staleTime: 0,
  });
}
export function useTask(key: string) {
  return useQuery({
    queryKey: adminKeys.task(key),
    queryFn: () => v2("GET /api/v2/admin/tasks/{key}", { path: { key } }),
    staleTime: 0,
  });
}
export function useTaskHistory(key: string) {
  const client = useQueryClient();
  const query = useInfiniteQuery({
    queryKey: [...adminKeys.taskHistory(key), "pages"],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      v2("GET /api/v2/admin/tasks/{key}/history", {
        path: { key },
        query: { limit: 20, cursor: pageParam },
      }),
    getNextPageParam: (page) => (page.page?.has_more ? page.page.next_cursor : undefined),
    staleTime: 0,
  });
  return {
    ...query,
    data: query.data?.pages.flatMap((page) => page.items),
    restart: () => client.resetQueries({ queryKey: adminKeys.taskHistory(key) }),
  };
}
export function useTaskMetrics(key: string) {
  const activity = usePageActivity();
  return useQuery({
    queryKey: adminKeys.taskMetrics(key),
    queryFn: () => v2("GET /api/v2/admin/tasks/{key}/metrics", { path: { key } }),
    enabled: key === "refresh_metadata",
    staleTime: 0,
    refetchInterval: activity.canApplyRealtimeUpdates ? 30_000 : false,
  });
}
export function useRunTask() {
  const client = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (key: string) =>
      v2("POST /api/v2/admin/tasks/{key}/run", { path: { key }, retryAuthentication: false }),
    onSuccess: (_, key) => {
      void client.invalidateQueries({ queryKey: adminKeys.tasks() });
      void client.invalidateQueries({ queryKey: adminKeys.task(key) });
      toast.success("Task started on this server");
    },
    onError: (error) => toast.error(taskMutationMessage(error)),
  });
}
export function useCancelTask() {
  const client = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (key: string) =>
      v2("POST /api/v2/admin/tasks/{key}/cancel", { path: { key }, retryAuthentication: false }),
    onSuccess: (_, key) => {
      void client.invalidateQueries({ queryKey: adminKeys.tasks() });
      void client.invalidateQueries({ queryKey: adminKeys.task(key) });
      toast.success("Cancellation requested on this server");
    },
    onError: (error) => toast.error(taskMutationMessage(error)),
  });
}
export function useUpdateTriggers() {
  const client = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      key,
      triggers,
      etag,
    }: {
      key: string;
      triggers: TriggerConfig[];
      etag: string;
    }) =>
      v2("PUT /api/v2/admin/tasks/{key}/triggers", {
        path: { key },
        headers: { "If-Match": etag },
        body: { triggers },
        retryAuthentication: false,
      }),
    onSuccess: (_, { key }) => {
      void client.invalidateQueries({ queryKey: adminKeys.task(key) });
      void client.invalidateQueries({ queryKey: adminKeys.tasks() });
      toast.success("Schedule saved and applied on this server. Other servers load it on restart.");
    },
    onError: (error) => toast.error(taskMutationMessage(error)),
  });
}
