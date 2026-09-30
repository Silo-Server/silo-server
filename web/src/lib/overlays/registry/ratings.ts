import { formatOutOfTen, formatPercent } from "@/components/ratings/ratings";

import type { OverlayDef } from "../types";

// A rating badge carries its source's mark in the label ("IMDb 8.5", "RT
// 93%"), the same plain-text mark title pages use, so a score is never shown
// without its source. Silo draws no source artwork: the owners of these
// scores restrict their logos.
function ratingLabel(mark: string, value: number | null | undefined, max: 10 | 100): string | null {
  if (value == null) return null;
  return `${mark} ${max === 100 ? formatPercent(value) : formatOutOfTen(value)}`;
}

export const RATINGS_OVERLAYS: readonly OverlayDef[] = [
  {
    id: "rating_imdb",
    category: "ratings",
    label: "IMDb Rating",
    description: "IMDb score out of 10",
    defaultPosition: "top-right",
    defaultEnabled: false,
    defaultAccent: "#f5c518",
    iconCapable: false,
    getValue: (d) => ratingLabel("IMDb", d.rating_imdb, 10),
  },
  {
    id: "rating_tmdb",
    category: "ratings",
    label: "TMDB Rating",
    description: "TMDB score out of 10",
    defaultPosition: "top-right",
    defaultEnabled: false,
    defaultAccent: "#01b4e4",
    iconCapable: false,
    getValue: (d) => ratingLabel("TMDB", d.rating_tmdb, 10),
  },
  {
    id: "rating_rt",
    category: "ratings",
    label: "RT Critics",
    description: "Rotten Tomatoes critic score, when an administrator shows it",
    defaultPosition: "top-right",
    defaultEnabled: false,
    defaultAccent: "#fa320a",
    iconCapable: false,
    ratingSource: "rt_critic",
    getValue: (d) => ratingLabel("RT", d.rating_rt_critic, 100),
  },
  {
    id: "rating_rt_audience",
    category: "ratings",
    label: "RT Audience",
    description: "Rotten Tomatoes audience score, when an administrator shows it",
    defaultPosition: "top-right",
    defaultEnabled: false,
    defaultAccent: "#fa6400",
    iconCapable: false,
    ratingSource: "rt_audience",
    getValue: (d) => ratingLabel("RT Audience", d.rating_rt_audience, 100),
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
