// Flags a `web.admin.*` message id in a file that isn't admin-only.
//
// Admin text is its own catalog chunk. It loads when the admin layout or the
// setup wizard mounts, or once an admin's session starts, and a non-admin never
// requests it. A `web.admin.*` message formatted anywhere else renders empty.
// Text a non-admin can see belongs in another namespace.
//
// eslint.config.js lists the admin-only paths; this rule runs on every other
// file. Tests are not checked: test-setup.ts preloads the admin part, so a
// test cannot tell.

const ADMIN_ID = /^web\.admin\.\S/;

const MESSAGE =
  "`web.admin.*` text loads only for admins. Use another namespace for text a non-admin can see, " +
  "or move this code to an admin-only path (see ADMIN_TEXT_PATHS in eslint.config.js).";

export default {
  meta: {
    type: "problem",
    schema: [],
    docs: { description: "Disallow web.admin.* message ids outside admin-only files" },
  },
  create(context) {
    return {
      Literal(node) {
        if (typeof node.value === "string" && ADMIN_ID.test(node.value)) {
          context.report({ node, message: MESSAGE });
        }
      },
      TemplateElement(node) {
        // `web.admin.${name}` builds an id; its static head is enough.
        if ((node.value.cooked ?? node.value.raw).startsWith("web.admin.")) {
          context.report({ node, message: MESSAGE });
        }
      },
    };
  },
};
