/**
 * A small Markdown reader for the setup and description text plugins ship in
 * their manifests. It understands paragraphs, bullet and numbered lists, and
 * inline **strong**, *emphasis*, `code`, and [links](https://…). Anything else,
 * HTML included, stays literal text: the output is plain data rendered as React
 * text nodes, so plugin-supplied text can never inject markup.
 */

export type PluginMarkdownInline =
  | { kind: "text"; text: string }
  | { kind: "strong"; text: string }
  | { kind: "em"; text: string }
  | { kind: "code"; text: string }
  | { kind: "link"; text: string; href: string };

export type PluginMarkdownBlock =
  | { kind: "paragraph"; inlines: PluginMarkdownInline[] }
  | { kind: "list"; ordered: boolean; items: PluginMarkdownInline[][] };

const MAX_SOURCE_LENGTH = 20_000;
const BULLET = /^\s*[-*+]\s+(.*)$/;
const NUMBERED = /^\s*\d+[.)]\s+(.*)$/;
// One alternation per inline form; the first match at a position wins.
// Underscore emphasis is left out: it would mangle snake_case identifiers
// such as `dump_path` that setup text mentions.
const INLINE = /\*\*([^*]+)\*\*|`([^`]+)`|\[([^\]]+)\]\(([^)\s]+)\)|\*([^*\s][^*]*)\*/g;

export function parsePluginMarkdown(source: string | undefined | null): PluginMarkdownBlock[] {
  const text = (source ?? "").slice(0, MAX_SOURCE_LENGTH).replace(/\r\n?/g, "\n").trim();
  if (!text) return [];

  const blocks: PluginMarkdownBlock[] = [];
  for (const chunk of text.split(/\n\s*\n/)) {
    const lines = chunk.split("\n").filter((line) => line.trim() !== "");
    if (lines.length === 0) continue;

    let paragraph: string[] = [];
    let list: { ordered: boolean; items: string[] } | null = null;
    const flushParagraph = () => {
      if (paragraph.length > 0) {
        blocks.push({ kind: "paragraph", inlines: parseInlines(paragraph.join(" ")) });
        paragraph = [];
      }
    };
    const flushList = () => {
      if (list) {
        blocks.push({ kind: "list", ordered: list.ordered, items: list.items.map(parseInlines) });
        list = null;
      }
    };

    for (const line of lines) {
      const bullet = BULLET.exec(line);
      const numbered = bullet ? null : NUMBERED.exec(line);
      const item = bullet ?? numbered;
      if (item) {
        const ordered = Boolean(numbered);
        flushParagraph();
        if (list && list.ordered !== ordered) flushList();
        list ??= { ordered, items: [] };
        list.items.push(item[1] ?? "");
      } else if (list && /^\s+/.test(line)) {
        // An indented line continues the previous list item.
        list.items[list.items.length - 1] += ` ${line.trim()}`;
      } else {
        flushList();
        paragraph.push(line.trim());
      }
    }
    flushParagraph();
    flushList();
  }
  return blocks;
}

export function parseInlines(text: string): PluginMarkdownInline[] {
  const inlines: PluginMarkdownInline[] = [];
  let last = 0;
  for (const match of text.matchAll(INLINE)) {
    const index = match.index ?? 0;
    if (index > last) inlines.push({ kind: "text", text: text.slice(last, index) });
    const [, strong, code, linkText, href, em] = match;
    if (strong) inlines.push({ kind: "strong", text: strong });
    else if (code) inlines.push({ kind: "code", text: code });
    else if (linkText && href) inlines.push({ kind: "link", text: linkText, href });
    else inlines.push({ kind: "em", text: em ?? "" });
    last = index + match[0].length;
  }
  if (last < text.length) inlines.push({ kind: "text", text: text.slice(last) });
  return inlines;
}
