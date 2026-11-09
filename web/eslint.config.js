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
  "src/components/AddToCollectionDialog.tsx",
  "src/components/admin/**",
  "src/components/AdminCatalogMaintenance.tsx",
  "src/components/AdminDownloadPreparationsRefresh.tsx",
  "src/components/AdminJobHistory.tsx",
  "src/components/AdminLayout.tsx",
  "src/components/AdminSectionCommandDialog.tsx",
  "src/components/adminSessionActionModel.ts",
  "src/components/AdminSessionActions.tsx",
  "src/components/AdminSidebar.tsx",
  "src/components/AdminUserDeleteDialog.tsx",
  "src/components/AdminUserImpersonationDialog.tsx",
  "src/components/AdminUserPasswordResetDialog.tsx",
  "src/components/AdminUserSetPasswordDialog.tsx",
  "src/components/advancedFilterOptions.ts",
  "src/components/AppSidebar*.{ts,tsx}",
  "src/components/audiobooks/**",
  "src/components/auth/**",
  "src/components/boxsets/**",
  "src/components/calendar/**",
  "src/components/calm/**",
  "src/components/CancelRequestDialog.tsx",
  "src/components/CardPlayOverlay.tsx",
  "src/components/CardWatchedBadge.tsx",
  "src/components/CastCarousel.tsx",
  "src/components/castCrewGroups.ts",
  "src/components/catalog/**",
  "src/components/collections/**",
  "src/components/CollectionTemplateGallery/**",
  "src/components/ConfirmDialog.tsx",
  "src/components/ContinueWatchingCard.tsx",
  "src/components/CrewList.tsx",
  "src/components/DownloadVersionPicker.tsx",
  "src/components/EditMetadataDialog.tsx",
  "src/components/EditMetadataDialog.form.ts",
  "src/components/EditPersonDialog.tsx",
  "src/components/EpisodeRow.tsx",
  "src/components/ErrorBoundary.tsx",
  "src/components/FilterBar.tsx",
  "src/components/FilterRuleEditor.tsx",
  "src/components/FolderBrowser.tsx",
  "src/components/GlobalSearch.tsx",
  "src/components/HeroBanner.tsx",
  "src/components/heroMetadata.ts",
  "src/components/HeroOverview.tsx",
  "src/components/HeroOverviewText.tsx",
  "src/components/homeRows/**",
  "src/components/ImageSelectorTab.tsx",
  "src/components/ImpersonationBanner.tsx",
  "src/components/ItemCard.tsx",
  "src/components/ItemGrid.tsx",
  "src/components/ItemLibraryPicker.tsx",
  "src/components/JellyfinSessionPill.tsx",
  "src/components/Layout.tsx",
  "src/components/LibraryAccessSelector.tsx",
  "src/components/LibraryEmptyState.tsx",
  "src/components/LibraryHeader.tsx",
  "src/components/LibraryMultiSelect.tsx",
  "src/components/LocalErrorBoundary.tsx",
  "src/components/MangaFilesDialog.tsx",
  "src/components/markers/**",
  "src/components/MatchItemDialog.tsx",
  "src/components/MediaCarousel.tsx",
  "src/components/MediaItemMenu.tsx",
  "src/components/MediaLocations.tsx",
  "src/components/MetadataTranslatePanel.tsx",
  "src/components/notifications/**",
  "src/components/NowListeningHero.tsx",
  "src/components/onboarding/**",
  "src/components/page/**",
  "src/components/PageBack.tsx",
  "src/components/PageUnavailable.tsx",
  "src/components/PasswordInput.tsx",
  "src/components/PathAutocompleteInput.tsx",
  "src/components/PlaybackRouteBadges.tsx",
  "src/components/policy/**",
  "src/components/profiles/**",
  "src/components/ratings/**",
  "src/components/RecommendationGrid.tsx",
  "src/components/RefreshMetadataDialog.tsx",
  "src/components/Request*.tsx",
  "src/components/SaveBar.tsx",
  "src/components/ScrollToTopButton.tsx",
  "src/components/SearchBar.tsx",
  "src/components/SeasonAccordion.tsx",
  "src/components/SectionItemCard.tsx",
  "src/components/SectionRow.tsx",
  "src/components/sections/**",
  "src/components/ServerActivity.tsx",
  "src/components/sessions/**",
  "src/components/settings/**",
  "src/components/SideNav.tsx",
  "src/components/SigningSecretDialog.tsx",
  "src/components/SiloBrand.tsx",
  "src/components/SplitItemDialog.tsx",
  "src/components/StarRating.tsx",
  "src/components/StreamBitrateLimitInput.tsx",
  "src/components/subtitles/**",
  "src/components/TasteSeedBanner.tsx",
  "src/components/TasteSeedGate.tsx",
  "src/components/ui/**",
  "src/components/UnreadableFileBadge.tsx",
  "src/components/UnsavedChangesGuard.tsx",
  "src/components/UserPolicyFields.tsx",
  "src/components/watchlist/**",
  "src/components/watchtogether/**",
  "src/hooks/admin/**",
  "src/hooks/queries/admin/branding.ts",
  "src/hooks/queries/admin/dashboardLayout.ts",
  "src/hooks/queries/admin/diagnostics.ts",
  "src/hooks/queries/admin/invitations.ts",
  "src/hooks/queries/admin/inviteCodes.ts",
  "src/hooks/queries/admin/networkAccess.ts",
  "src/hooks/queries/admin/nodes.ts",
  "src/hooks/queries/admin/overlayImages.ts",
  "src/hooks/queries/admin/plugins.ts",
  "src/hooks/queries/admin/serverNotificationChannels.ts",
  "src/hooks/queries/admin/settings.ts",
  "src/hooks/queries/admin/tasks.ts",
  "src/hooks/queries/homeDismissals.ts",
  "src/hooks/queries/homeRows/useHomeLayoutExport.ts",
  "src/hooks/queries/items.ts",
  "src/hooks/queries/people.ts",
  "src/hooks/queries/watchlist.ts",
  "src/lib/adminNavigation.ts",
  "src/lib/adminSettingsSearch.ts",
  "src/lib/aiProviders.ts",
  "src/lib/boxSets.ts",
  "src/lib/collections/**",
  "src/lib/episodeCardLabels.ts",
  "src/lib/externalSignInAdmin.ts",
  "src/lib/extraKinds.ts",
  "src/lib/historyRemoval.ts",
  "src/lib/homeLayoutTransfer.ts",
  "src/lib/homeRows/**",
  "src/lib/pluginCapabilities.ts",
  "src/lib/pluginPresentation.ts",
  "src/lib/pluginStatus.ts",
  "src/lib/pluginStatusIndicator.ts",
  "src/lib/taskProgress.ts",
  "src/lib/taskTrigger.ts",
  "src/lib/upcomingEventPresentation.ts",
  "src/pages/ActivateDevice.tsx",
  "src/pages/admin-backups/**",
  "src/pages/admin-downloads/**",
  "src/pages/admin-policy/**",
  "src/pages/admin-requests/**",
  "src/pages/admin-settings/**",
  "src/pages/admin-users/**",
  "src/pages/admin/autoscan/**",
  "src/pages/AdminAccessGroups.tsx",
  "src/pages/AdminActivity.tsx",
  "src/pages/adminActivityPresentation.ts",
  "src/pages/AdminApiKeys.tsx",
  "src/pages/AdminAutoscan.tsx",
  "src/pages/AdminAutoscanActivity.tsx",
  "src/pages/AdminBackupRestore.tsx",
  "src/pages/AdminBackups.tsx",
  "src/pages/AdminCollections.tsx",
  "src/pages/AdminDashboard.tsx",
  "src/pages/AdminDevices.tsx",
  "src/pages/AdminDiagnostics.tsx",
  "src/pages/AdminDiagnostics.filters.ts",
  "src/pages/AdminDiagnostics.parts.tsx",
  "src/pages/AdminDownloads.tsx",
  "src/pages/AdminHistoryImport.tsx",
  "src/pages/AdminHomeRows.tsx",
  "src/pages/AdminLibraries.tsx",
  "src/pages/AdminLogs.tsx",
  "src/pages/AdminLogs.format.ts",
  "src/pages/AdminLogsPlaybackSummary.tsx",
  "src/pages/adminLogsFilters.ts",
  "src/pages/AdminMaintenance.tsx",
  "src/pages/AdminMarkerHistory.tsx",
  "src/pages/AdminNodes.tsx",
  "src/pages/adminNodesPresentation.ts",
  "src/pages/AdminPlaybackHistory.tsx",
  "src/pages/AdminPluginDetail.tsx",
  "src/pages/AdminPlugins.tsx",
  "src/pages/AdminRecommendations.tsx",
  "src/pages/AdminRecommendationsEmbedding.tsx",
  "src/pages/AdminRequestDiscover.tsx",
  "src/pages/AdminRequests.tsx",
  "src/pages/AdminStats.tsx",
  "src/pages/AdminSubtitles.tsx",
  "src/pages/AdminTaskDetail.tsx",
  "src/pages/AdminTaskTriggerForm.tsx",
  "src/pages/AdminTasks.tsx",
  "src/pages/AdminUserDetail.tsx",
  "src/pages/AdminUsers.tsx",
  "src/pages/audiobooks/**",
  "src/pages/BoxSetPage.tsx",
  "src/pages/Calendar.tsx",
  "src/pages/Catalog.tsx",
  "src/pages/ChoosePassword.tsx",
  "src/pages/CollectionEditorPage.tsx",
  "src/pages/Collections.tsx",
  "src/pages/Collections.cards.tsx",
  "src/pages/EbookReader.tsx",
  "src/pages/ForgotPassword.tsx",
  "src/pages/Home.tsx",
  "src/pages/homeSectionCache.ts",
  "src/pages/homeSectionQueue.ts",
  "src/pages/homeSectionState.ts",
  "src/pages/homeSurfaceRefresh.ts",
  "src/pages/HouseholdSetup.tsx",
  "src/pages/InviteClaim.tsx",
  "src/pages/ItemDetail/**",
  "src/pages/LegacyAdminHomeRowsRedirect.tsx",
  "src/pages/LegacyRequestDetailRedirect.tsx",
  "src/pages/LibraryBrowse.tsx",
  "src/pages/LibraryCollections.tsx",
  "src/pages/LibraryPage.tsx",
  "src/pages/LibraryRecommended.tsx",
  "src/pages/Login.tsx",
  "src/pages/Notifications.tsx",
  "src/pages/OAuthComplete.tsx",
  "src/pages/PasswordReset.tsx",
  "src/pages/PersonDetail.tsx",
  "src/pages/Profiles.tsx",
  "src/pages/Recommendations.tsx",
  "src/pages/RecommendationsSection.tsx",
  "src/pages/Request*.tsx",
  "src/pages/sectionDedupe.ts",
  "src/pages/settings/**",
  "src/pages/SettingsLayout.tsx",
  "src/pages/setup-wizard/**",
  "src/pages/SetupWizard.tsx",
  "src/pages/Signup.tsx",
  "src/pages/TasteSeed.tsx",
  "src/pages/TitleDetail.tsx",
  "src/pages/WatchRoute.tsx",
  "src/pages/watchtogether/**",
  "src/playback/**",
  "src/player/**",
  "src/reader/**",
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
    // Test fixtures render literal text on purpose.
    ignores: ["**/*.test.*", "**/*.test-support.*"],
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
