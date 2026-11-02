import { Book, Film, Headphones, Tv } from "lucide-react";
import { cn } from "@/lib/utils";

const TV_TYPES = new Set(["series", "season", "episode"]);
// "book" and "books" are audiobooks, as on the Apple client.
const AUDIO_TYPES = new Set(["audiobook", "book", "books", "podcast", "podcasts"]);
const READING_TYPES = new Set(["ebook", "ebooks", "manga", "comic", "comics"]);

/**
 * The mark for an item type: a TV for series, seasons and episodes,
 * headphones for audiobooks and podcasts, a book for ebooks, manga and
 * comics, and a film for movies and anything else, including an unknown type.
 */
function TypeMark({ mediaType }: { mediaType?: string | null }) {
  const props = { "aria-hidden": true, focusable: false, className: "default-artwork-mark" };
  if (mediaType && TV_TYPES.has(mediaType)) return <Tv {...props} />;
  if (mediaType && AUDIO_TYPES.has(mediaType)) return <Headphones {...props} />;
  if (mediaType && READING_TYPES.has(mediaType)) return <Book {...props} />;
  return <Film {...props} />;
}

/**
 * What a poster, cover or still shows when the title has no artwork: a faint
 * Silo-colour glow with a small mark for the item type. The Apple and Android
 * clients draw the same thing, so keep `.default-artwork` (app.css) and the
 * type mapping in step with them. Decoration only: the card names the title.
 * Fills its positioned parent.
 */
export default function DefaultArtwork({
  mediaType,
  className,
}: {
  /** The item's type, e.g. "movie", "series", "episode" or "audiobook". */
  mediaType?: string | null;
  className?: string;
}) {
  return (
    <div aria-hidden="true" className={cn("default-artwork absolute inset-0", className)}>
      <TypeMark mediaType={mediaType} />
    </div>
  );
}
