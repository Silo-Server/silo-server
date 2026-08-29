import { useMediaQuery } from "@/hooks/useMediaQuery";

const QUERY = "(pointer: coarse)";

export function useCoarsePointer(): boolean {
  return useMediaQuery(QUERY);
}
