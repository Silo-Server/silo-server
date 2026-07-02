import { StreamLanguage } from "@codemirror/language";

const KEYWORDS = new Set([
  "package",
  "import",
  "default",
  "if",
  "else",
  "not",
  "in",
  "every",
  "some",
  "as",
  "with",
  "contains",
]);

const ATOMS = new Set(["true", "false", "null"]);
const OPERATORS = "{}[](),.;:=+-*/%<>!|&";

export const regoLanguage = StreamLanguage.define({
  name: "rego",
  token(stream) {
    if (stream.eatSpace()) {
      return null;
    }

    if (stream.match("#")) {
      stream.skipToEnd();
      return "comment";
    }

    if (stream.peek() === '"') {
      stream.next();
      let escaped = false;
      let ch: string | undefined;
      while ((ch = stream.next()) !== undefined) {
        if (ch === '"' && !escaped) {
          break;
        }
        escaped = ch === "\\" && !escaped;
        if (ch !== "\\") {
          escaped = false;
        }
      }
      return "string";
    }

    if (stream.match(/-?(?:\d+\.\d+|\d+|\.\d+)/)) {
      return "number";
    }

    if (stream.match(/[A-Za-z_][A-Za-z0-9_]*/)) {
      const word = stream.current();
      if (KEYWORDS.has(word)) {
        return "keyword";
      }
      if (ATOMS.has(word)) {
        return "atom";
      }
      return "variableName";
    }

    if (stream.eatWhile((ch) => OPERATORS.includes(ch))) {
      return "operator";
    }

    stream.next();
    return null;
  },
});
