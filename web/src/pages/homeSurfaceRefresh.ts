import { useQuery } from "@tanstack/react-query";
import type { QueryClient } from "@tanstack/react-query";
import { mediaSurfaceKeys } from "@/hooks/queries/keys";

export function useSectionRefreshSignal() {
  return useQuery({
    queryKey: mediaSurfaceKeys.refreshSignal(),
    queryFn: () => 0,
    initialData: 0,
    staleTime: Number.POSITIVE_INFINITY,
    gcTime: Number.POSITIVE_INFINITY,
  });
}

export function bumpHomeRefreshSignal(queryClient: QueryClient) {
  queryClient.setQueryData<number>(
    mediaSurfaceKeys.refreshSignal(),
    (current) => (current ?? 0) + 1,
  );
}
