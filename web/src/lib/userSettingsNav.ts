import {
  ArrowDownUp,
  Bell,
  Bookmark,
  Cast,
  Clapperboard,
  Clock,
  Cloud,
  Eye,
  KeyRound,
  LayoutDashboard,
  Library,
  MonitorSmartphone,
  PanelTop,
  Play,
  Server,
  ShieldCheck,
  Sparkles,
  Subtitles,
  Users,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

import { appIntl, englishText, t, type MessageKey } from "@/i18n";

export interface UserSettingsNavItem {
  path: string;
  label: string;
  icon: LucideIcon;
  description: string;
  keywords?: readonly string[];
  /** Every control on the page, by its visible label, so search can find it. */
  settings?: readonly { label: string; description?: string; keywords?: readonly string[] }[];
  primaryOrAdmin?: boolean;
  /** Listed only while the server has requests turned on. */
  requiresRequests?: boolean;
}

export interface UserSettingsNavGroup {
  label: string;
  items: UserSettingsNavItem[];
}

/** A page in the table below: its text as message ids, translated when read. */
interface UserSettingsNavSource {
  path: string;
  label: MessageKey;
  icon: LucideIcon;
  description: MessageKey;
  /** English search terms; they match in every language. */
  keywords?: readonly string[];
  /** Every control on the page, by the id of its visible label, so search can find it. */
  settings?: readonly MessageKey[];
  primaryOrAdmin?: boolean;
  requiresRequests?: boolean;
}

interface UserSettingsNavGroupSource {
  label: MessageKey;
  items: UserSettingsNavSource[];
}

/**
 * The English text of `ids` that the current language shows differently, so
 * a search typed in English still finds a page in any language.
 */
function englishTerms(...ids: MessageKey[]): string[] {
  // In English every term would equal its label; skip formatting each twice.
  if (appIntl().locale === "en") return [];
  return ids.flatMap((id) => {
    const english = englishText(id);
    return english === t(id) ? [] : [english];
  });
}

/** A page with its text in the current language; English stays searchable. */
function navItem(group: MessageKey, source: UserSettingsNavSource): UserSettingsNavItem {
  return {
    path: source.path,
    icon: source.icon,
    label: t(source.label),
    description: t(source.description),
    keywords: [
      ...(source.keywords ?? []),
      ...englishTerms(group, source.label, source.description),
    ],
    settings: source.settings?.map((id) => {
      const keywords = englishTerms(id);
      return keywords.length > 0 ? { label: t(id), keywords } : { label: t(id) };
    }),
    primaryOrAdmin: source.primaryOrAdmin,
    requiresRequests: source.requiresRequests,
  };
}

/**
 * Grouped by the question a person arrives with, not by which service stores
 * the value: "how does it play", "how does it look", "what do I see", "what is
 * it wired to", "who am I". Your Devices sits under Playback because nearly
 * every setting on it is a device's playback override; Libraries sits with Home
 * Screen because both decide what the app puts in front of you.
 *
 * Paths are stable route segments: the server links to some of them (the
 * notification emails to `notifications`, sign-in linking to `account`, Plex
 * sign-in to `webhook-sync`), so rename labels freely but keep paths.
 */
const USER_SETTINGS_GROUPS: UserSettingsNavGroupSource[] = [
  {
    label: "web.settings.nav.group.playback",
    items: [
      {
        path: "playback",
        label: "web.settings.playback.title",
        icon: Play,
        description: "web.settings.nav.playback.description",
        keywords: [
          "video quality",
          "resolution",
          "bitrate",
          "bandwidth",
          "auto skip",
          "intro",
          "credits",
          "recap",
          "auto play",
          "preview",
          "rewind",
          "fast forward",
          "fast-forward",
          "skip",
          "seek",
          "seek controls",
          "skip interval",
          "audiobook skip",
        ],
        settings: [
          "settingsManifest.playback.preferred_quality.label",
          "settingsManifest.playback.max_bitrate_kbps.label",
          "settingsManifest.playback.intro_skip_mode.label",
          "settingsManifest.playback.auto_skip_recap.label",
          "settingsManifest.playback.auto_skip_credits.label",
          "web.settings.playback.nextEpisode.previewLabel",
          "settingsManifest.playback.auto_play_next.label",
          "web.settings.playback.seek.title",
          "web.settings.playback.seek.rewindInterval",
          "web.settings.playback.seek.forwardInterval",
          "web.settings.playback.legacy.title",
        ],
      },
      {
        path: "subtitle-appearance",
        label: "web.settings.subtitleAppearance.title",
        icon: Subtitles,
        description: "web.settings.nav.subtitleAppearance.description",
        keywords: [
          "language",
          "audio language",
          "spoken language",
          "audio track",
          "metadata language",
          "translation",
          "subtitles",
          "subtitle language",
          "forced subtitles",
          "captions",
          "font size",
          "font color",
          "background",
          "position",
          "timing",
          "subtitle delay",
        ],
        settings: [
          "web.settings.subtitleAppearance.language.spokenLabel",
          "web.settings.metadataLanguage.label",
          "web.settings.metadataLanguage.addException",
          "web.settings.subtitleAppearance.subtitles.languageLabel",
          "web.settings.subtitleAppearance.subtitles.behaviorLabel",
          "settingsManifest.playback.show_forced_subtitles.label",
          "web.settings.subtitleAppearance.preview.title",
          "web.settings.subtitleAppearance.preview.save",
          "web.settings.subtitleAppearance.preview.discard",
          "web.settings.subtitleAppearance.text.fontSize",
          "web.settings.subtitleAppearance.text.fontFamily",
          "web.settings.subtitleAppearance.text.fontColor",
          "web.settings.subtitleAppearance.text.opacity",
          "web.settings.subtitleAppearance.text.outline",
          "web.settings.subtitleAppearance.text.outlineColor",
          "web.settings.subtitleAppearance.background.style",
          "web.settings.subtitleAppearance.background.opacity",
          "web.settings.subtitleAppearance.background.color",
          "web.settings.subtitleAppearance.background.position",
          "web.settings.subtitleAppearance.timing.offset",
        ],
      },
      {
        path: "devices",
        label: "web.settings.device.title",
        icon: MonitorSmartphone,
        description: "web.settings.nav.devices.description",
        keywords: [
          "devices",
          "tv",
          "phone",
          "tablet",
          "browser",
          "this device",
          "forget device",
          "hdr",
          "dolby vision",
          "sound delay",
          "lip sync",
          "who's watching",
          "profile picker",
          "remember profile",
          "launch",
          "override",
        ],
        settings: [
          "web.settings.profileLaunch.label",
          "web.settings.profileLaunch.remember",
          "web.settings.profileLaunch.ask",
          "web.settings.device.useProfileSettings",
          "web.settings.device.forget",
          "settingsManifest.playback.preferred_quality.label",
          "settingsManifest.playback.max_bitrate_kbps.label",
          "settingsManifest.player.hdr_enabled.label",
          "settingsManifest.player.dolby_vision_enabled.label",
          "settingsManifest.player.dv_profile7_hdr10_fallback.label",
          "settingsManifest.player.match_frame_rate.label",
          "settingsManifest.player.video_gravity.label",
          "settingsManifest.player.orientation_mode.label",
          "settingsManifest.playback.audio_language.label",
          "settingsManifest.ui.theme_music_enabled.label",
          "settingsManifest.player.audio_sync_ms.label",
          "settingsManifest.player.playback_speed.label",
          "settingsManifest.playback.subtitle_language.label",
          "settingsManifest.playback.subtitle_appearance.label",
          "settingsManifest.player.subtitle_sync_ms.label",
          "settingsManifest.playback.intro_skip_mode.label",
          "settingsManifest.playback.auto_play_next.label",
          "settingsManifest.playback.auto_play_next_preview.label",
          "settingsManifest.player.sleep_timer_default_minutes.label",
          "settingsManifest.ui.title_art.label",
        ],
      },
    ],
  },
  {
    label: "web.settings.nav.group.appearance",
    items: [
      {
        path: "interface",
        label: "web.settings.interface.title",
        icon: PanelTop,
        description: "web.settings.nav.interface.description",
        keywords: [
          "navigation",
          "menu",
          "pin library",
          "poster size",
          "card size",
          "poster",
          "badges",
          "overlay",
          "card overlays",
          "accent color",
          "corner",
          "position",
          "quick actions",
          "favorite",
          "watched",
          "server defaults",
        ],
        settings: [
          "web.settings.interface.preview.title",
          "web.settings.interface.postersTitle",
          "web.settings.interface.posterSize",
          "web.settings.cardOverlay.shortcutsTitle",
          "web.settings.cardOverlay.badgesTitle",
          "web.settings.cardOverlay.showBadges",
          "web.settings.cardOverlay.style",
          "web.settings.nav.setting.mediaInfo",
          "web.settings.nav.setting.ratingsCertifications",
          "web.settings.nav.setting.contentMetadata",
          "web.settings.nav.setting.statusAwards",
          "web.settings.cardOverlay.adjust",
          "web.settings.nav.setting.corner",
          "web.settings.cardOverlay.color",
          "web.settings.cardOverlay.showIcon",
          "web.settings.interface.menuTitle",
          "web.settings.interface.addLink",
          "web.settings.resetButton.label",
        ],
      },
      {
        path: "title-pages",
        label: "web.settings.titlePages.title",
        icon: Clapperboard,
        description: "web.settings.nav.titlePages.description",
        keywords: [
          "title art",
          "logo",
          "clearlogo",
          "theme music",
          "detail page",
          "spoilers",
          "blur",
          "unwatched",
          "advisory age",
          "age rating",
          "common sense media",
        ],
        settings: [
          "settingsManifest.ui.title_art.label",
          "web.settings.titleArt.allDevices",
          "settingsManifest.ui.theme_music_enabled.label",
          "settingsManifest.ui.theme_music_loop.label",
          "settingsManifest.catalog.hide_unwatched_episode_images.label",
          "settingsManifest.catalog.hide_unwatched_episode_overviews.label",
          "settingsManifest.catalog.show_advisory_age.label",
        ],
      },
      {
        path: "accessibility",
        label: "web.settings.accessibility.title",
        icon: Eye,
        description: "web.settings.nav.accessibility.description",
        keywords: [
          "contrast",
          "readability",
          "text",
          "date format",
          "time format",
          "clock",
          "24-hour",
          "12-hour",
        ],
        settings: [
          "web.settings.accessibility.textSize",
          "web.settings.accessibility.textWeight",
          "web.settings.accessibility.contrast",
          "web.settings.accessibility.contrastHigh",
          "web.settings.accessibility.previewTitle",
          "web.settings.accessibility.dateTimeTitle",
          "web.settings.accessibility.dateFormat",
          "web.settings.accessibility.timeFormat",
        ],
      },
    ],
  },
  {
    label: "web.settings.nav.group.homeDiscovery",
    items: [
      {
        path: "home-screen",
        label: "web.settings.homeScreen.title",
        icon: LayoutDashboard,
        description: "web.settings.nav.homeScreen.description",
        keywords: [
          "home rows",
          "sections",
          "rows",
          "continue watching",
          "next up",
          "library pages",
          "hero banner",
        ],
        settings: [
          "web.settings.nav.setting.homeRows",
          "web.settings.nav.setting.addRow",
          "settingsManifest.home.hide_watched_items.label",
          "settingsManifest.ui.next_up_mode.label",
          "web.settings.homeScreen.export",
          "web.settings.homeScreen.import",
          "web.settings.nav.setting.resetHome",
          "web.settings.nav.setting.continueWatching",
          "web.settings.nav.setting.nextUp",
          "web.settings.nav.setting.recentlyAdded",
        ],
      },
      {
        path: "libraries",
        label: "web.settings.library.title",
        icon: Library,
        description: "web.settings.nav.libraries.description",
        keywords: [
          "library visibility",
          "access",
          "disabled libraries",
          "library order",
          "playback preferences",
        ],
        settings: [
          "web.settings.library.rememberPages.label",
          "web.settings.nav.setting.libraryVisibility",
          "web.settings.library.visibleInNavigation",
          "web.settings.library.showAll",
          "web.settings.library.hideAll",
          "settingsManifest.ui.library_order.label",
          "web.settings.library.spokenLanguage",
          "web.settings.library.subtitleLanguage",
          "web.settings.library.subtitleBehavior",
          "web.settings.library.forcedSubtitles",
          "web.settings.nav.setting.playbackPreferences",
        ],
      },
      {
        path: "personalize",
        label: "web.settings.personalize.title",
        icon: Sparkles,
        description: "web.settings.nav.personalize.description",
        keywords: [
          "taste profile",
          "recommendations",
          "ratings",
          "likes",
          "dislikes",
          "not interested",
          "hidden",
          "tour",
        ],
        settings: [
          "web.settings.personalize.refineTitle",
          "web.settings.nav.setting.tasteProfile",
          "web.settings.nav.setting.recommendations",
          "web.settings.notInterestedList.title",
          "web.settings.personalize.tourTitle",
        ],
      },
      {
        path: "requests",
        label: "web.settings.requests.title",
        icon: Bookmark,
        description: "web.settings.nav.requests.description",
        keywords: ["watchlist", "request", "discover", "auto request"],
        settings: ["settingsManifest.requests.watchlist_auto_request.label"],
        requiresRequests: true,
      },
    ],
  },
  {
    label: "web.settings.nav.group.connections",
    items: [
      {
        path: "connect-apps",
        label: "web.settings.connectApps.title",
        icon: Cast,
        description: "web.settings.nav.connectApps.description",
        keywords: [
          "jellyfin",
          "infuse",
          "swiftfin",
          "jellycon",
          "findroid",
          "compatibility",
          "sign in",
          "login",
          "server address",
          "username",
          "pin",
        ],
        settings: [
          "web.settings.connectApps.appType.silo",
          "web.settings.connectApps.appType.jellyfin",
          "web.settings.connectApps.field.server",
          "web.settings.connectApps.field.username",
          "web.settings.connectApps.field.password",
          "web.settings.connectApps.profilePicker.title",
          "web.settings.connectApps.troubleshooting.title",
          "web.settings.connectApps.glance.title",
        ],
      },
      {
        path: "watch-providers",
        label: "web.settings.watchProviders.title",
        icon: Cloud,
        description: "web.settings.nav.watchProviders.description",
        keywords: [
          "simkl",
          "import",
          "export",
          "scrobble",
          "favorites",
          "watch history",
          "watchlist",
          "ratings",
        ],
        settings: [
          "web.settings.watchProviders.stats.lastImported",
          "web.settings.watchProviders.stats.lastExported",
          "web.settings.watchProviders.stats.watched",
          "web.settings.watchProviders.stats.progress",
          "web.settings.watchProviders.stats.favorites",
          "web.settings.watchProviders.stats.watchlist",
          "web.settings.watchProviders.stats.ratings",
          "web.settings.watchProviders.stats.exported",
          "web.settings.watchProviders.toggle.importWatched.label",
          "web.settings.watchProviders.toggle.importProgress.label",
          "web.settings.watchProviders.toggle.exportWatched.label",
          "web.settings.nav.setting.sendUnwatched",
          "web.settings.watchProviders.toggle.favorites.label",
          "web.settings.watchProviders.toggle.favoriteRemovals.label",
          "web.settings.watchProviders.toggle.watchlist.label",
          "web.settings.watchProviders.toggle.watchlistOrder.label",
          "web.settings.watchProviders.toggle.importRatings.label",
          "web.settings.watchProviders.toggle.exportRatings.label",
          "web.settings.watchProviders.toggle.syncDropped.label",
          "web.settings.watchProviders.toggle.scrobble.label",
        ],
      },
      {
        path: "webhook-sync",
        label: "web.settings.webhooks.title",
        icon: Server,
        description: "web.settings.nav.webhookSync.description",
        keywords: ["plex", "emby", "jellyfin", "webhook", "progress", "watched", "live"],
        settings: [
          "web.settings.webhooks.add.title",
          "web.settings.webhooks.servers.title",
          "web.settings.webhooks.detail.webhookUrl",
          "web.settings.webhooks.defaultProfile",
          "web.settings.webhooks.mapping.title",
          "web.settings.webhooks.deliveries.title",
        ],
      },
      {
        path: "history-import",
        label: "web.settings.historyImport.title",
        icon: Clock,
        description: "web.settings.nav.historyImport.description",
        keywords: ["emby", "plex", "jellyfin", "watched history", "import", "mapping", "migrate"],
        settings: [
          "web.settings.historyImport.newImport.title",
          "web.settings.historyImport.importIntoProfile",
          "web.settings.historyImport.startImport",
          "web.settings.historyImport.latestImport.title",
          "web.settings.historyImport.history.title",
          "web.settings.historyImport.metric.fetched",
          "web.settings.historyImport.metric.matched",
          "web.settings.historyImport.metric.unmatched",
          "web.settings.historyImport.metric.skipped",
        ],
      },
    ],
  },
  {
    label: "web.settings.nav.group.account",
    items: [
      {
        path: "notifications",
        label: "web.settings.notifications.title",
        icon: Bell,
        description: "web.settings.notifications.subtitle",
        keywords: ["new episodes", "email", "discord", "browser push", "webhooks", "alerts"],
        settings: [
          "web.settings.notifications.episodes.title",
          "web.settings.notifications.reason.favorites",
          "web.settings.notifications.reason.watchlist",
          "web.settings.notifications.reason.continueWatching",
          "web.settings.notifications.reason.nextUp",
          "web.settings.notifications.push.title",
          "web.settings.notifications.email.title",
          "web.settings.notifications.email.deliverTo",
          "web.settings.notifications.frequency.label",
          "web.settings.nav.setting.digest",
          "web.settings.notifications.discord.title",
          "web.settings.notifications.webhooks.title",
          "web.settings.webhooks.detail.webhookUrl",
        ],
      },
      {
        path: "your-data",
        label: "web.settings.yourData.title",
        icon: ArrowDownUp,
        description: "web.settings.yourData.subtitle",
        keywords: ["export", "import", "download", "backup", "move", "migrate", "json", "my data"],
        settings: [
          "web.settings.yourData.export.title",
          "web.settings.yourData.import.title",
          "web.settings.yourData.import.chooseFile",
        ],
      },
      {
        path: "profiles",
        label: "web.settings.profiles.title",
        icon: Users,
        description: "web.settings.nav.profiles.description",
        keywords: [
          "profile name",
          "pin",
          "access",
          "primary profile",
          "household",
          "kids",
          "parental controls",
        ],
        settings: [
          "web.settings.profiles.newProfile",
          "web.settings.nav.setting.profileName",
          "web.profiles.pin.label",
          "web.profiles.editor.kids",
          "web.profiles.editor.maxContentRating",
          "web.profiles.editor.maxAdvisoryAge",
          "web.profiles.editor.maxPlaybackQuality",
          "web.profiles.editor.restrictLibraries",
          "web.profiles.streams.heading",
          "web.settings.profiles.deleteTitle",
          "web.settings.nav.setting.primaryProfile",
        ],
        primaryOrAdmin: true,
      },
      {
        path: "account",
        label: "web.settings.account.title",
        icon: KeyRound,
        description: "web.settings.nav.account.description",
        keywords: ["password", "credential", "sign in", "security", "account", "sso"],
        settings: [
          "web.settings.account.password.current",
          "web.settings.account.password.new",
          "web.settings.account.password.confirm",
          "web.settings.account.password.change",
          "web.settings.accountSignIn.title",
          "web.settings.accountSignIn.connect",
          "web.settings.accountSignIn.disconnect",
        ],
        primaryOrAdmin: true,
      },
      {
        path: "sessions",
        label: "web.settings.nav.sessions.label",
        icon: ShieldCheck,
        description: "web.settings.nav.sessions.description",
        primaryOrAdmin: true,
        keywords: [
          "sessions",
          "active",
          "client",
          "device",
          "last seen",
          "sign out",
          "revoke",
          "security",
        ],
        settings: [
          "web.settings.nav.setting.activeSignIns",
          "web.settings.nav.setting.thisBrowser",
          "web.settings.nav.setting.otherSessions",
          "web.settings.nav.setting.signOutEverywhere",
        ],
      },
    ],
  },
];

export interface UserSettingsNavAccess {
  /** Admins and primary profiles manage the household's sign-in and profiles. */
  canManageProfiles: boolean;
  /** Whether the server has requests turned on. */
  requestsEnabled: boolean;
}

/**
 * The groups this profile can open, with empty groups dropped, in the current
 * language. Call it again after a language change.
 */
export function visibleUserSettingsGroups({
  canManageProfiles,
  requestsEnabled,
}: UserSettingsNavAccess): UserSettingsNavGroup[] {
  return USER_SETTINGS_GROUPS.map((group) => ({
    label: t(group.label),
    items: group.items
      .filter(
        (item) =>
          (!item.primaryOrAdmin || canManageProfiles) &&
          (!item.requiresRequests || requestsEnabled),
      )
      .map((item) => navItem(group.label, item)),
  })).filter((group) => group.items.length > 0);
}

export function userSettingsHref(path: string): string {
  return `/settings/${path}`;
}
