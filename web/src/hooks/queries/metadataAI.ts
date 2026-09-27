import { useQuery } from "@tanstack/react-query";

import { v2 } from "@/api/v2/request";

export type MetadataAIOnViewMode = "off" | "button" | "auto";

export interface MetadataAIStatus {
  /** Whether the server has metadata AI translation configured and enabled. */
  enabled: boolean;
  /** The viewer-facing on-view translation mode. */
  on_view: MetadataAIOnViewMode;
}

function onViewMode(value: string): MetadataAIOnViewMode {
  return value === "auto" || value === "button" ? value : "off";
}

export async function fetchMetadataAIStatus(
  options?: Pick<RequestInit, "signal">,
): Promise<MetadataAIStatus> {
  const capability = await v2("GET /api/v2/capabilities/metadata-ai", {
    signal: options?.signal ?? undefined,
  });
  return {
    enabled: capability.state === "available",
    on_view: onViewMode(capability.on_view),
  };
}

/** Whether the server has metadata AI translation configured, and the
 * viewer-facing on-view mode. */
export function useMetadataAIStatus(enabled = true) {
  return useQuery({
    queryKey: ["metadata-ai", "status"],
    queryFn: ({ signal }) => fetchMetadataAIStatus({ signal }),
    staleTime: 5 * 60 * 1000,
    enabled,
  });
}
