// Shared by the rules that rewrite Tailwind class tokens in place.

/**
 * Splits a class token into its variants (`md:`, `data-[state=open]:`,
 * `[&:hover]:`), `!` before or after (v3 and v4 forms), and the utility. A
 * colon inside brackets, as in `text-[length:13px]`, belongs to its segment.
 */
export function classParts(token) {
  const [, variants, lead, utility, trail] =
    /^((?:(?:[^\s:"'`[\]]|\[[^\]]*\])+:)*)(!?)(.*?)(!?)$/.exec(token);
  return { variants, lead, utility, trail };
}

// Tokens are separated by whitespace, quotes, or template-literal joints.
const TOKEN = /(?<=^|[\s"'`}])([^\s"'`${}]+)(?=$|[\s"'`$])/g;

/**
 * Visitors that check every string literal and template segment. `convert`
 * returns a token's replacement, or null to leave it; each node with any
 * replacement is reported once under `messageId`, listing them, with a fix.
 */
export function classTokenVisitors(context, convert, messageId) {
  function check(node) {
    const text = context.sourceCode.getText(node);
    const found = [];
    const fixed = text.replace(TOKEN, (token) => {
      const replacement = convert(token);
      if (replacement === null) return token;
      found.push([token, replacement]);
      return replacement;
    });
    if (found.length === 0) return;
    context.report({
      node,
      messageId,
      data: { list: found.map(([from, to]) => `${from} → ${to}`).join(", ") },
      fix: (fixer) => fixer.replaceText(node, fixed),
    });
  }
  return {
    Literal(node) {
      if (typeof node.value === "string") check(node);
    },
    TemplateElement: check,
  };
}
