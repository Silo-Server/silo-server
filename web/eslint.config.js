import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import eslintConfigPrettier from "eslint-config-prettier";
import eslintComments from "@eslint-community/eslint-plugin-eslint-comments";

export default tseslint.config(
  { ignores: ["dist"] },
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
    // Requests infer their shape through the generated v2 contract.
    files: ["**/*.{ts,tsx}"],
    rules: {
      "no-restricted-syntax": [
        "error",
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
      ],
    },
  },
  eslintConfigPrettier,
);
