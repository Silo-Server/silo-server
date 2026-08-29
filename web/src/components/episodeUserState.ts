import type { MediaItemUserState } from "@/api/types";

export function toEpisodeUserState(
  userData: { played?: boolean } | null | undefined,
): MediaItemUserState {
  return {
    played: userData?.played ?? false,
    is_favorite: false,
    in_watchlist: false,
  };
}
