import { RuleTester } from "eslint";
import tseslint from "typescript-eslint";
import { afterAll, describe, it } from "vitest";

import rule from "./no-admin-message-outside-admin.js";

RuleTester.afterAll = afterAll;
RuleTester.describe = describe;
RuleTester.it = it;

const tester = new RuleTester({
  languageOptions: {
    parser: tseslint.parser,
    parserOptions: { ecmaFeatures: { jsx: true } },
  },
});

tester.run("no-admin-message-outside-admin", rule, {
  valid: [
    `t("web.common.shell.loadingPage");`,
    `t("web.settings.devices.title");`,
    // The prefix on its own is not a message id.
    `const ADMIN_MESSAGE_PREFIX = "web.admin.";`,
    `const note = "see web.admin.settings for the keys";`,
  ],
  invalid: [
    { code: `t("web.admin.common.title.tab.admin");`, errors: 1 },
    { code: `const key = \`web.admin.common.title.tab.\${name}\`;`, errors: 1 },
    { code: `type Key = \`web.admin.common.title.header.\${string}\`;`, errors: 1 },
    { code: `const ids = { a: "web.admin.a.b", b: "web.admin.c.d" };`, errors: 2 },
  ],
});
