import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import eslintConfigPrettier from "eslint-config-prettier";
import eslintComments from "@eslint-community/eslint-plugin-eslint-comments";
import formatjs from "eslint-plugin-formatjs";

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

// Files and directories whose text is extracted to the message catalog. An
// extraction PR adds its area here, one glob per line, sorted.
const I18N_CONVERTED = [
  "src/components/AppSidebar*.{ts,tsx}",
  "src/components/auth/**",
  "src/components/calendar/DayGroup.tsx",
  "src/components/calm/**",
  "src/components/ConfirmDialog.tsx",
  "src/components/ErrorBoundary.tsx",
  "src/components/homeRows/AddToOtherLibrariesDialog.tsx",
  "src/components/ImpersonationBanner.tsx",
  "src/components/Layout.tsx",
  "src/components/LocalErrorBoundary.tsx",
  "src/components/onboarding/**",
  "src/components/page/**",
  "src/components/PageBack.tsx",
  "src/components/PageUnavailable.tsx",
  "src/components/PasswordInput.tsx",
  "src/components/SaveBar.tsx",
  "src/components/ScrollToTopButton.tsx",
  "src/components/SideNav.tsx",
  "src/components/SiloBrand.tsx",
  "src/components/ui/**",
  "src/components/UnsavedChangesGuard.tsx",
  "src/pages/ActivateDevice.tsx",
  "src/pages/ChoosePassword.tsx",
  "src/pages/ForgotPassword.tsx",
  "src/pages/HouseholdSetup.tsx",
  "src/pages/InviteClaim.tsx",
  "src/pages/Login.tsx",
  "src/pages/OAuthComplete.tsx",
  "src/pages/PasswordReset.tsx",
  "src/pages/setup-wizard/**",
  "src/pages/SetupWizard.tsx",
  "src/pages/Signup.tsx",
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
    files: ["src/i18n/**"],
    rules: { "no-restricted-imports": "off" },
  },
  {
    files: I18N_CONVERTED,
    // Tests render fixtures with literal text; the guard is for shipped UI.
    ignores: ["**/*.test.{ts,tsx}"],
    plugins: { formatjs },
    rules: {
      "formatjs/no-literal-string-in-jsx": [
        "error",
        {
          props: {
            include: [
              ["*", "{label,title,subtitle,description,help,placeholder,currentNote,alt,tooltip}"],
            ],
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
