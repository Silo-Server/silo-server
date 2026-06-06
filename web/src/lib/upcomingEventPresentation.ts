interface UpcomingPresentationEvent {
  type: "movie" | "episode" | "season_premiere";
  air_date: string;
  air_time?: string | null;
  air_at?: string | null;
  episode_title?: string | null;
  season_number?: number | null;
  episode_number?: number | null;
  badges: string[];
}

export function upcomingBadgeLabel(badge: string): string {
  switch (badge) {
    case "series_premiere":
      return "Series Premiere";
    case "season_premiere":
      return "Season Premiere";
    case "finale":
      return "Finale";
    default:
      return badge;
  }
}

export function upcomingBadgeClass(badge: string): string {
  switch (badge) {
    case "series_premiere":
    case "season_premiere":
      return "border-primary/40 bg-primary/20 text-primary";
    case "finale":
      return "border-orange-800/55 bg-orange-950/90 text-orange-50 shadow-sm backdrop-blur-none";
    default:
      return "border-border/50 bg-surface";
  }
}

export function formatUpcomingSubtitle(event: UpcomingPresentationEvent): string {
  if (
    (event.type === "episode" || event.type === "season_premiere") &&
    event.season_number != null
  ) {
    if (event.episode_number != null) {
      return `S${event.season_number} · E${event.episode_number}${
        event.episode_title ? ` - ${event.episode_title}` : ""
      }`;
    }
    return `Season ${event.season_number}${event.episode_title ? ` · ${event.episode_title}` : ""}`;
  }

  if (event.type === "movie") {
    return "Movie";
  }

  return "";
}

export function formatUpcomingDate(airDate: string): string {
  return new Date(`${airDate}T00:00:00`).toLocaleDateString(undefined, {
    weekday: "short",
    month: "short",
    day: "numeric",
  });
}

export function formatUpcomingTime(airTime?: string | null, airAt?: string | null): string | null {
  if (airAt) {
    const date = new Date(airAt);
    if (!Number.isNaN(date.getTime())) {
      return date.toLocaleTimeString(undefined, {
        hour: "numeric",
        minute: "2-digit",
      });
    }
  }
  if (!airTime) {
    return null;
  }
  return new Date(`2000-01-01T${airTime}`).toLocaleTimeString(undefined, {
    hour: "numeric",
    minute: "2-digit",
  });
}
