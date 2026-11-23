import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import eslintConfigPrettier from "eslint-config-prettier";
import eslintComments from "@eslint-community/eslint-plugin-eslint-comments";
import formatjs from "eslint-plugin-formatjs";
import noAdminMessage from "./eslint-rules/no-admin-message-outside-admin.js";
import noPhysicalDirection from "./eslint-rules/no-physical-direction.js";
import textSizeInRem from "./eslint-rules/text-size-in-rem.js";

// Requests infer their shape through the generated v2 contract.
const CONTRACT_SYNTAX = [
  {
    selector: "CallExpression[callee.name='api'][typeArguments]",
    message:
      "api<T>(...) asserts a response shape. Migrated files call v2(...) and let the contract infer it.",
  },
  {
    selector: "CallExpression[callee.name='apiWithProfileRequestContext'][typeArguments]",
    message:
      "apiWithProfileRequestContext<T>(...) asserts a response shape. Migrated files call v2(...) and let the contract infer it.",
  },
  {
    selector: "TSAsExpression > TSTypeReference > Identifier[name=/(Response|Request)$/]",
    message:
      "Casting to a *Response/*Request type bypasses the contract. Use the inferred v2 types instead.",
  },
];

// Translated text goes through src/i18n (see its README).
const REACT_INTL_IMPORT = {
  name: "react-intl",
  message: "Use useT()/t() from @/i18n; only src/i18n talks to react-intl.",
};
const PLAIN_TEXT_IMPORT = {
  name: "@/i18n",
  importNames: ["t", "serverText"],
  message:
    "Components use useT()/useServerText(), which re-render on a language change; t() and serverText() are for plain code.",
};

const I18N_TEXT_PROPS =
  "{label,title,subtitle,description,help,placeholder,currentNote,alt,tooltip,unit," +
  "hint,detail,message,caption,eyebrow,heading,summary,note,empty," +
  "*Label,*Text,*Hint,*Note,*Message,*Title,*Heading,*Description,*Summary,*Caption}";

// Every source file's text comes from the message catalog. src/i18n builds the
// formatting layer itself; tests render literal text on purpose.
const I18N_CONVERTED = ["src/**/*.{ts,tsx}"];
const I18N_UNCHECKED = ["src/i18n/**", "src/locales/**", "src/test/**", "src/test-setup.ts"];

// Repository rules. One object, so every config block that enables a rule
// registers the same plugin.
const silo = {
  rules: { "no-physical-direction": noPhysicalDirection, "text-size-in-rem": textSizeInRem },
};

// Files that draw physical geometry, where left means left in every language:
// media timelines measured with clientX, chart plots, badges placed on poster
// artwork. Everything else uses logical utilities (ps-, ms-, start-,
// text-start…) so the layout mirrors right to left. A single literal line
// elsewhere takes an eslint-disable comment with its reason.
const PHYSICAL_DIRECTION = [
  "src/components/admin/dashboard/charts/chartChrome.tsx",
  "src/components/admin/dashboard/charts/LineChart.tsx",
  "src/components/admin/dashboard/charts/StackedColumnChart.tsx",
  "src/components/overlays/CardOverlays.tsx",
  "src/player/components/SeekBar.tsx",
  "src/player/components/VolumeControl.tsx",
];

// Files that may use `web.admin.*` messages: the admin area, the setup wizard
// and modules only they import. Admin text is its own catalog chunk that a
// non-admin never loads (see src/i18n/README.md), so any other file must use a
// different namespace. A file that mixes admin and shared text is listed only
// when what non-admin screens call from it uses no admin key.
const ADMIN_TEXT_PATHS = [
  // Admin pages, components, hooks and API clients.
  "src/api/admin*",
  "src/api/v2/admin*",
  "src/components/[Aa]dmin*",
  "src/components/admin/**",
  "src/hooks/admin/**",
  "src/hooks/queries/admin/**",
  "src/hooks/queries/homeRows/useAdminHomeRows.ts",
  "src/lib/admin*",
  "src/pages/Admin*",
  "src/pages/admin*",
  "src/pages/admin*/**",
  // The setup wizard waits for the admin part (useAdminMessages).
  "src/components/FolderBrowser.tsx",
  "src/pages/setup-wizard/**",
  "src/pages/SetupWizard.tsx",
  // Surfaces an admin sees outside the admin layout; each waits for the part
  // with useAdminMessages({ optional: true }).
  "src/components/ServerActivity.tsx",
  // Helpers only admin pages import.
  "src/hooks/queries/useAutoscan.ts",
  // Admin settings pages and the setup wizard.
  "src/hooks/useSettingsForm.ts",
  "src/lib/accessGroupPolicyChanges.ts",
  "src/lib/accountOwner.ts",
  "src/lib/aiProviders.ts",
  "src/lib/externalSignInAdmin.ts",
  "src/lib/plugin*.ts",
  "src/lib/policyDiagnostics.ts",
  "src/lib/requestAccess.ts",
  "src/lib/requestRoutingOptions.ts",
  "src/lib/scanRuns.ts",
  "src/lib/taskProgress.ts",
  "src/lib/taskText.ts",
  "src/lib/taskTrigger.ts",
  // Shared with non-admin screens, which call only the parts without admin keys:
  // documentTitle's resolveDocumentTitle, profileDataTypes' DATA_TYPE_LABELS,
  // dataCountsPhrase and titleList, sections' profile hooks.
  "src/hooks/queries/sections.ts",
  "src/lib/documentTitle.ts",
  "src/lib/profileDataTypes.ts",
];

export default tseslint.config(
  { ignores: ["dist", "src/locales"] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ["**/*.{ts,tsx}"],
    // Typed rules below need this block; their trials remain disabled.
    // languageOptions: {
    //   parserOptions: { projectService: true, tsconfigRootDir: import.meta.dirname },
    // },
    plugins: {
      "react-hooks": reactHooks,
      "react-refresh": reactRefresh,
      "eslint-comments": eslintComments,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "react-refresh/only-export-components": ["warn", { allowConstantExport: true }],
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_" },
      ],
      "@typescript-eslint/no-explicit-any": "error",
      "@typescript-eslint/no-empty-object-type": [
        "error",
        { allowInterfaces: "with-single-extends" },
      ],
      "eslint-comments/require-description": "error",
      "@typescript-eslint/adjacent-overload-signatures": "error",
      "@typescript-eslint/ban-tslint-comment": "error",
      "@typescript-eslint/class-literal-property-style": "error",
      "@typescript-eslint/consistent-type-assertions": "error",
      "@typescript-eslint/no-confusing-non-null-assertion": "error",
      "@typescript-eslint/no-inferrable-types": "error",
      "@typescript-eslint/no-non-null-asserted-nullish-coalescing": "error",
      "@typescript-eslint/prefer-function-type": "error",
      "@typescript-eslint/prefer-literal-enum-member": "error",
      "@typescript-eslint/unified-signatures": "error",
      // "@typescript-eslint/array-type": "error", // Disabled: 265 full-tree findings.
      // "@typescript-eslint/consistent-generic-constructors": "error", // Disabled: 5 full-tree findings.
      // "@typescript-eslint/consistent-indexed-object-style": "error", // Disabled: 9189 full-tree findings.
      // "@typescript-eslint/consistent-type-definitions": "error", // Disabled: 142 full-tree findings.
      // "@typescript-eslint/no-dynamic-delete": "error", // Disabled: 16 full-tree findings.
      // "@typescript-eslint/no-empty-function": "error", // Disabled: 706 full-tree findings.
      // "@typescript-eslint/no-extraneous-class": "error", // Disabled: 1 full-tree findings.
      // "@typescript-eslint/no-invalid-void-type": "error", // Disabled: 11 full-tree findings.
      // "@typescript-eslint/no-non-null-assertion": "error", // Disabled: 1736 full-tree findings.
      // "@typescript-eslint/no-useless-constructor": "error", // Disabled: 2 full-tree findings.
      // "@typescript-eslint/prefer-for-of": "error", // Disabled: 1 full-tree findings.
      // Typed rules: not enrolled; prior measurements below, no new cost campaign.
      // "@typescript-eslint/no-floating-promises": ["error", { ignoreVoid: true }], // TEST: 199 production; ignoreVoid false: 519.
      // "@typescript-eslint/no-misused-promises": ["error", { checksVoidReturn: true }], // TEST: 135 production; JSX attributes off: 6.
      // "@typescript-eslint/await-thenable": "error", // TEST: 2 production.
      // Unsafe group: prior production/all counts 26/170, 9/330, 10/2510, 4/418; origins need review.
      // "@typescript-eslint/no-unsafe-argument": "error", // TEST: prior group counts above.
      // "@typescript-eslint/no-unsafe-assignment": "error", // TEST: prior group counts above.
      // "@typescript-eslint/no-unsafe-call": "error", // TEST: prior group counts above.
      // "@typescript-eslint/no-unsafe-member-access": "error", // TEST: prior group counts above.
      // "@typescript-eslint/no-unsafe-return": "error", // TEST: prior group counts above.
      // "@typescript-eslint/switch-exhaustiveness-check": "error", // TEST: 6 production / 56 all.
      // "@typescript-eslint/no-unnecessary-condition": "error", // Declined: 663 production findings.
      // Other large typed style rules: declined; prior counts 436, 223, 663, 261.
      "react-hooks/exhaustive-deps": "error",
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/set-state-in-effect": "error",
      "react-hooks/refs": "error",
      "react-hooks/preserve-manual-memoization": "error",
    },
  },
  {
    files: ["**/*.{ts,tsx}"],
    rules: {
      "no-restricted-syntax": ["error", ...CONTRACT_SYNTAX],
      "no-restricted-imports": ["error", { paths: [REACT_INTL_IMPORT] }],
    },
  },
  {
    files: ["**/*.tsx"],
    rules: {
      "no-restricted-imports": ["error", { paths: [REACT_INTL_IMPORT, PLAIN_TEXT_IMPORT] }],
    },
  },
  {
    files: ["src/**/*.{ts,tsx}"],
    // Tests assert whatever classes the source uses.
    ignores: [...PHYSICAL_DIRECTION, "**/*.test.*", "**/*.test-support.*"],
    plugins: { silo },
    rules: { "silo/no-physical-direction": "error" },
  },
  {
    files: ["src/**/*.{ts,tsx}"],
    ignores: ["**/*.test.*", "**/*.test-support.*"],
    plugins: { silo },
    rules: { "silo/text-size-in-rem": "error" },
  },
  {
    files: ["src/**/*.{ts,tsx}"],
    // test-setup.ts preloads the admin part, so a test can't tell.
    ignores: [...ADMIN_TEXT_PATHS, "src/i18n/**", "**/*.test.*", "**/*.test-support.*"],
    plugins: { "silo-admin-text": { rules: { "no-admin-message": noAdminMessage } } },
    rules: { "silo-admin-text/no-admin-message": "error" },
  },
  {
    files: ["src/i18n/**"],
    rules: { "no-restricted-imports": "off" },
  },
  {
    files: I18N_CONVERTED,
    // Test fixtures render literal text on purpose.
    ignores: [...I18N_UNCHECKED, "**/*.test.*", "**/*.test-support.*"],
    plugins: { formatjs },
    rules: {
      "formatjs/no-literal-string-in-jsx": [
        "error",
        {
          props: {
            // Props that carry text a person reads. The suffix globs catch the
            // many component props named for what they show (confirmLabel,
            // emptyText, ...); className, variant, id and similar stay out.
            include: [["*", I18N_TEXT_PROPS]],
            // An HTML attribute whose value is a keyboard keyword, not text.
            exclude: [["*", "enterKeyHint"]],
          },
        },
      ],
      "no-restricted-syntax": [
        "error",
        ...CONTRACT_SYNTAX,
        {
          selector:
            "CallExpression:matches([callee.name='toast'], [callee.object.name='toast'])[arguments.0.type=/^(Literal|TemplateLiteral)$/]",
          message: "Toast text comes from the message catalog: toast.success(t(...)).",
        },
      ],
    },
  },
  eslintConfigPrettier,
);
