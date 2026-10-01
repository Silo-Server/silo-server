import type { OverlayDef } from "../types";

function formatRating(
  value: number | null | undefined,
  max: number,
  suffix?: string,
): string | null {
  if (value == null) return null;
  if (max === 100) return `${value}%${suffix ? ` ${suffix}` : ""}`;
  return `${value.toFixed(1)}${suffix ? ` ${suffix}` : ""}`;
}

export const RATINGS_OVERLAYS: readonly OverlayDef[] = [
  {
    id: "rating_imdb",
    category: "ratings",
    label: "IMDb Rating",
    description: "IMDb score out of 10",
    defaultPosition: "top-right",
    defaultEnabled: false,
    iconId: "star",
    defaultAccent: "#f5c518",
    iconCapable: true,
    getValue: (d) => formatRating(d.rating_imdb, 10),
  },
  {
    id: "rating_tmdb",
    category: "ratings",
    label: "TMDB Rating",
    description: "TMDB score out of 10",
    defaultPosition: "top-right",
    defaultEnabled: false,
    iconId: "star",
    defaultAccent: "#01b4e4",
    iconCapable: true,
    getValue: (d) => formatRating(d.rating_tmdb, 10),
  },
  {
    id: "rating_rt",
    category: "ratings",
    label: "RT Critics",
    description: "Rotten Tomatoes critic score",
    defaultPosition: "top-right",
    defaultEnabled: false,
    iconId: "tomato",
    defaultAccent: "#fa320a",
    iconCapable: true,
    getValue: (d) => formatRating(d.rating_rt_critic, 100),
  },
  {
    id: "rating_rt_audience",
    category: "ratings",
    label: "RT Audience",
    description: "Rotten Tomatoes audience score",
    defaultPosition: "top-right",
    defaultEnabled: false,
    iconId: "tomato",
    defaultAccent: "#fa6400",
    iconCapable: true,
    getValue: (d) => formatRating(d.rating_rt_audience, 100),
  },
  {
    id: "content_rating",
    category: "ratings",
    label: "Age Rating",
    description: "Content rating (PG-13, TV-MA, R, etc.)",
    defaultPosition: "bottom-right",
    defaultEnabled: false,
    iconId: "shield",
    iconCapable: true,
    getValue: (d) => d.content_rating ?? null,
  },
  {
    id: "advisory_age",
    category: "ratings",
    label: "Advisory Age",
    description: "Recommended minimum viewer age, such as Common Sense Media's 13+",
    defaultPosition: "bottom-right",
    defaultEnabled: false,
    iconId: "users",
    iconCapable: true,
    introducedInManifest: 13,
    getValue: (d) => (d.advisory_age != null && d.advisory_age > 0 ? `${d.advisory_age}+` : null),
  },
];
