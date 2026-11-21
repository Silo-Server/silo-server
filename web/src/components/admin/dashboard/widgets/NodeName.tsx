import { startsRightToLeft } from "@/lib/direction";

/** Enough to keep a worker's number and role, as in "-4 Transcode". */
const TAIL_LENGTH = 12;
/** The longest end kept when a right-to-left or mixed name needs a word boundary. */
const MAX_TAIL_LENGTH = 16;

// The right-to-left blocks, as RTL_CHAR in lib/direction.ts.
const RIGHT_TO_LEFT =
  /[\u0590-\u08ff\ufb1d-\ufdff\ufe70-\ufeff\u{10800}-\u{10fff}\u{1e800}-\u{1efff}]/u;
const STARTS_RIGHT_TO_LEFT = new RegExp(`^${RIGHT_TO_LEFT.source}`, "u");
const ENDS_RIGHT_TO_LEFT = new RegExp(`${RIGHT_TO_LEFT.source}$`, "u");
const STARTS_WITH_LETTER = /^\p{L}/u;
// A mark belongs to the letter before it; a lone low surrogate to the character before it.
const STARTS_ATTACHED = /^[\p{M}\udc00-\udfff]/u;
// Marks, tatweel and the zero-width joiner join a letter to the one after them.
const ENDS_JOINING = /[\p{M}\u0640\u200d]$/u;

/**
 * Where to cut a name so that its two pieces, laid out in the name's own
 * direction, read exactly as the whole name does. A name with no right-to-left
 * characters can be cut anywhere outside a character. Otherwise the end piece
 * must start with a letter of the name's direction, so it resolves on its own
 * as it does inside the name and no part of a mixed name changes places, and
 * the cut must not separate right-to-left letters, which may join. Tries the
 * usual length, then longer, then shorter ends; `null` when the name is short
 * or has no such point.
 */
function nodeNameCut(name: string, rightToLeft: boolean) {
  if (name.length <= TAIL_LENGTH) return null;
  const leftToRightOnly = !RIGHT_TO_LEFT.test(name);
  const longest = Math.min(MAX_TAIL_LENGTH, name.length - 1);
  const lengths = [];
  for (let n = TAIL_LENGTH; n <= longest; n++) lengths.push(n);
  for (let n = TAIL_LENGTH - 1; n > 0; n--) lengths.push(n);
  for (const n of lengths) {
    const head = name.slice(0, -n);
    const tail = name.slice(-n);
    if (STARTS_ATTACHED.test(tail) || ENDS_JOINING.test(head)) continue;
    if (
      leftToRightOnly ||
      (STARTS_WITH_LETTER.test(tail) &&
        STARTS_RIGHT_TO_LEFT.test(tail) === rightToLeft &&
        !ENDS_RIGHT_TO_LEFT.test(head))
    ) {
      return { head, tail };
    }
  }
  return null;
}

/**
 * A name shortened in the middle when it does not fit. Workers tend to share
 * their start and differ at the end, so cutting the end would make rows look alike.
 * The name keeps its own direction, so on a page in the other direction its
 * pieces stay in reading order; one with no safe cut is cut at its end.
 */
export function NodeName({ name }: { name: string }) {
  const rightToLeft = startsRightToLeft(name);
  const cut = nodeNameCut(name, rightToLeft);
  return (
    <span className="flex min-w-0 font-semibold" dir={rightToLeft ? "rtl" : "ltr"} title={name}>
      {cut ? (
        <>
          <span className="min-w-0 overflow-hidden text-ellipsis whitespace-pre">{cut.head}</span>
          <span className="flex-shrink-0 whitespace-pre">{cut.tail}</span>
        </>
      ) : (
        <span className="min-w-0 overflow-hidden text-ellipsis whitespace-pre">{name}</span>
      )}
    </span>
  );
}
