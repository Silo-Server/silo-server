import { useContext, useEffect, useLayoutEffect, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import type { ItemDetail } from "@/api/types";
import { v2 } from "@/api/v2/request";
import { useOptionalAuth } from "@/hooks/useAuth";
import { useEffectiveSettings } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { THEME_MUSIC_INTERRUPT_EVENT, ThemeMusic, themeAudioFormats } from "@/lib/themeMusic";
import { WatchPlaybackControllerContext } from "@/playback/watchPlaybackContext";
import { useAudiobookPlaybackController } from "@/pages/audiobooks/player/audiobookPlaybackContext";

const keys = [SETTING_KEYS.UI_THEME_MUSIC_ENABLED, SETTING_KEYS.UI_THEME_MUSIC_LOOP];

export function useThemeMusic(item: ItemDetail | undefined, loading: boolean) {
  const auth = useOptionalAuth();
  const authority = `${auth?.user?.id ?? ""}:${auth?.profile?.id ?? ""}`;
  const { data: settings } = useEffectiveSettings({ keys });
  const enabled = settings?.[SETTING_KEYS.UI_THEME_MUSIC_ENABLED]?.value === true;
  const loop = settings?.[SETTING_KEYS.UI_THEME_MUSIC_LOOP]?.value === true;
  const watch = useContext(WatchPlaybackControllerContext);
  const audiobook = useAudiobookPlaybackController();
  const playing = Boolean(watch?.state.request || audiobook?.activeRequest);
  const capability = useQuery({
    queryKey: ["theme-songs-capability", authority],
    queryFn: ({ signal }) => v2("GET /api/v2/catalog/themes/capabilities", { signal }),
    enabled: enabled && Boolean(auth?.profile),
    staleTime: 60_000,
  });
  const player = useRef<ThemeMusic | null>(null);
  const suppressedOwner = useRef<string | undefined>(undefined);
  const owner = useRef<string | undefined>(undefined);
  useLayoutEffect(() => {
    owner.current = item?.themes?.owner_id;
  }, [item?.themes?.owner_id]);

  useEffect(() => {
    const context = captureProfileRequestContext();
    const music = new ThemeMusic(async (owner, theme, signal) => {
      const current = captureProfileRequestContext();
      if (
        !context ||
        !current ||
        current.authContextVersion !== context.authContextVersion ||
        current.profileId !== context.profileId ||
        current.serverOrigin !== context.serverOrigin
      )
        throw new Error("Profile changed");
      const formats = themeAudioFormats();
      const grant = await v2("POST /api/v2/catalog/items/{id}/themes/{theme_id}/playback", {
        path: { id: owner, theme_id: theme },
        body: formats.length > 0 ? { accepted_formats: formats } : undefined,
        signal,
        profileContext: current,
      });
      if (!isCapturedProfileAuthorityActive(current)) throw new Error("Profile changed");
      return { url: grant.url, delivery: grant.delivery };
    });
    player.current = music;
    return () => {
      music.stop(!context || !isCapturedProfileAuthorityActive(context));
      if (player.current === music) player.current = null;
    };
  }, [authority]);

  useEffect(() => {
    if (item?.themes && suppressedOwner.current !== item.themes.owner_id) {
      suppressedOwner.current = undefined;
    }
    if (
      !enabled ||
      playing ||
      !auth?.profile ||
      capability.data?.state !== "available" ||
      capability.data?.allowed === false ||
      (item?.themes?.owner_id !== undefined && suppressedOwner.current === item.themes.owner_id)
    ) {
      player.current?.stop(playing);
    } else if (loading) {
      player.current?.suspend();
    } else {
      player.current?.select(item?.themes, loop);
    }
  }, [
    authority,
    enabled,
    playing,
    auth?.profile,
    capability.data?.state,
    capability.data?.allowed,
    item,
    loading,
    loop,
  ]);

  useEffect(() => {
    // Iframe media events cannot reach this document, so trailers signal explicitly.
    const stop = () => {
      suppressedOwner.current = owner.current;
      player.current?.stop(true);
    };
    document.addEventListener("play", stop, true);
    document.addEventListener(THEME_MUSIC_INTERRUPT_EVENT, stop);
    return () => {
      document.removeEventListener("play", stop, true);
      document.removeEventListener(THEME_MUSIC_INTERRUPT_EVENT, stop);
    };
  }, []);
}
