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
// A mark belongs to the letter before it, a lone low surrogate to the character
// before it, and the zero-width joiner joins the characters on either side.
const STARTS_ATTACHED = /^[\p{M}\udc00-\udfff\u200d]/u;
// Marks, tatweel and the zero-width joiner join a letter to the one after them.
const ENDS_JOINING = /[\p{M}\u0640\u200d]$/u;
// The right-to-left mark, and the embeddings, overrides and isolates, whose
// effect would end at the cut.
const RIGHT_TO_LEFT_MARK = /\u200f/u;
const BIDI_CONTROL = /[\u202a-\u202e\u2066-\u2069]/u;
// What sets a name's direction for dir="auto": its first letter or directional mark.
const FIRST_STRONG = /[\p{L}\u200e\u200f\u061c]/u;

// Opening and closing punctuation, without the quotation marks and vertical
// forms that the bidi algorithm doesn't pair.
const NOT_BRACKET = "(?![\u201a\u201e\u2e42\u301d-\u301f\ufd3e\ufd3f\ufe17\ufe18\ufe35-\ufe48])";
const OPENS = new RegExp(`^${NOT_BRACKET}\\p{Ps}$`, "u");
const CLOSES = new RegExp(`^${NOT_BRACKET}\\p{Pe}$`, "u");
// The two pairs that aren't one or two code points apart, by closing bracket.
const CROSSED: Record<string, string> = { "\u298e": "\u298f", "\u2990": "\u298d" };
// Marks and format characters, such as a directional mark, which letters join
// across. Not the zero-width non-joiner and joiner, which set the joining.
const TRANSPARENT = /^(?![\u200c\u200d])[\p{M}\p{Cf}]$/u;

/** Whether `closing` pairs with `opening`: "()" and "（）" are one code point apart, "[]" two. */
function closes(closing: string, opening: string) {
  const crossed = CROSSED[closing];
  if (crossed !== undefined) return opening === crossed;
  const gap = closing.codePointAt(0)! - opening.codePointAt(0)!;
  return gap === 1 || gap === 2;
}

/** `head` without the marks and format characters at its end. */
function withoutTransparentEnd(head: string) {
  let end = head.length;
  while (end > 0) {
    const low = head.charCodeAt(end - 1);
    const size = end > 1 && low >= 0xdc00 && low <= 0xdfff ? 2 : 1;
    if (!TRANSPARENT.test(head.slice(end - size, end))) break;
    end -= size;
  }
  return head.slice(0, end);
}

/**
 * Whether a bracket opened in `head` is still open at its end, pairing brackets
 * as the bidi algorithm does: a closing bracket closes the nearest matching
 * opening one and anything opened after it, and one with no match is ignored.
 */
function leavesBracketOpen(head: string) {
  const open: string[] = [];
  for (const char of head) {
    if (OPENS.test(char)) open.push(char);
    else if (CLOSES.test(char)) {
      let at = open.length - 1;
      while (at >= 0 && !closes(char, open[at]!)) at--;
      if (at >= 0) open.length = at;
    }
  }
  return open.length > 0;
}

/**
 * Where to cut a name so that its two pieces, laid out in the name's own
 * direction, read exactly as the whole name does. A name with no right-to-left
 * characters can be cut anywhere outside a character. Otherwise the end piece
 * must start with a letter of the name's direction, so it resolves on its own
 * as it does inside the name and no part of a mixed name changes places, the
 * cut must not separate right-to-left letters, which may join, and it must not
 * fall inside brackets, which pair across the whole name. A right-to-left mark
 * makes a name mixed; a bidi control that the cut would end means no cut. Tries
 * the usual length, then longer, then shorter ends; `null` when the name is
 * short or has no such point.
 */
function nodeNameCut(name: string, rightToLeft: boolean) {
  if (name.length <= TAIL_LENGTH || BIDI_CONTROL.test(name)) return null;
  const leftToRightOnly = !RIGHT_TO_LEFT.test(name) && !RIGHT_TO_LEFT_MARK.test(name);
  // A name whose first letter startsRightToLeft judges differently from the
  // blocks above, such as one starting in Syriac or with a tatweel, isn't cut.
  const first = FIRST_STRONG.exec(name)?.[0];
  if (
    first !== undefined &&
    RIGHT_TO_LEFT.test(first) !== rightToLeft &&
    !RIGHT_TO_LEFT_MARK.test(first)
  ) {
    return null;
  }
  const longest = Math.min(MAX_TAIL_LENGTH, name.length - 1);
  const lengths = [];
  for (let n = TAIL_LENGTH; n <= longest; n++) lengths.push(n);
  for (let n = TAIL_LENGTH - 1; n > 0; n--) lengths.push(n);
  for (const n of lengths) {
    const head = name.slice(0, -n);
    const tail = name.slice(-n);
    // The head without what letters join across, to check what it joins to.
    const bare = withoutTransparentEnd(head);
    if (STARTS_ATTACHED.test(tail) || ENDS_JOINING.test(head) || ENDS_JOINING.test(bare)) continue;
    if (
      leftToRightOnly ||
      (STARTS_WITH_LETTER.test(tail) &&
        STARTS_RIGHT_TO_LEFT.test(tail) === rightToLeft &&
        !ENDS_RIGHT_TO_LEFT.test(bare) &&
        !leavesBracketOpen(head))
    ) {
      return { head, tail };
    }
  }
  return null;
}

/**
 * A name shortened in the middle when it does not fit. Workers tend to share
 * their start and differ at the end, so cutting the end would make rows look alike.
 * A cut name keeps its own direction, so on a page in the other direction its
 * pieces stay in reading order. One with no safe cut is cut at its end, in the
 * direction the browser gives it. The end piece is never wider than the box, so
 * it can't run under the icons beside it; past that it loses its start.
 */
export function NodeName({ name }: { name: string }) {
  const rightToLeft = startsRightToLeft(name);
  const cut = nodeNameCut(name, rightToLeft);
  return (
    <span
      className="flex min-w-0 font-semibold"
      dir={cut ? (rightToLeft ? "rtl" : "ltr") : "auto"}
      title={name}
    >
      {cut ? (
        <>
          <span className="min-w-0 overflow-hidden text-ellipsis whitespace-pre">{cut.head}</span>
          {/* Wider than the box, the end piece loses its start, not the worker's number. */}
          <span className="flex max-w-full flex-shrink-0 justify-end overflow-hidden">
            <span className="whitespace-pre">{cut.tail}</span>
          </span>
        </>
      ) : (
        <span className="min-w-0 overflow-hidden text-ellipsis whitespace-pre">{name}</span>
      )}
    </span>
  );
}
