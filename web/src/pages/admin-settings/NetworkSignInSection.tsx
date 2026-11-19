import { useId, useRef, useState } from "react";
import { useIsMutating } from "@tanstack/react-query";
import { Link } from "react-router";

import type { PluginInstallation } from "@/api/types";
import { FeedbackLine, type Feedback } from "@/components/admin/FeedbackLine";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { Switch } from "@/components/ui/switch";
import {
  signInBindingMutationKey,
  useUpdateSignInBinding,
} from "@/hooks/queries/admin/externalSignIn";
import {
  adminSignInErrorText,
  authBindingOf,
  authProviderLabel,
  networkSignInInstallations,
  savedAutoProvision,
  signInBindingWrite,
} from "@/lib/externalSignInAdmin";
import { pluginPagePath } from "@/lib/pluginPresentation";
import { useT } from "@/i18n";

import { FieldGroup } from "./FieldGroup";
import { SettingFieldRow } from "./SettingField";

const GRANT_EXAMPLE = `"grants": [
  {"src": ["group:family-admins"], "dst": ["tag:silo"],
   "app": {"siloserver.org/cap/silo": [{"role": "admin"}]}},
  {"src": ["autogroup:member", "autogroup:shared"], "dst": ["tag:silo"],
   "app": {"siloserver.org/cap/silo": [{"role": "user"}]}}
]`;

// Code quoted in the explanation: the tailnet capability and the grants' JSON.
const CAPABILITY = "siloserver.org/cap/silo";
const ADMIN_ROLE = `{"role":"admin"}`;
const USER_ROLE = `{"role":"user"}`;

function code(chunks: React.ReactNode[]) {
  return <code className="font-mono">{chunks}</code>;
}

/**
 * Settings → Sign-in: network sign-in plugins (such as Tailscale), which sign
 * in the owner of the device a request came from with no password. One can
 * be on beside the server's OIDC or LDAP provider. Turning it on or off and
 * account creation apply at once, like the provider slot's switch. Who may
 * sign in, and with which role, is the tailnet policy's (the plugin's "Who
 * can sign in" setting and siloserver.org/cap/silo grants); see
 * docs/architecture/external-sign-in.md#network-identity. The two switches
 * stay in view; how grants, roles and shared devices work is reference reading
 * behind a disclosure.
 */
export function NetworkSignInSection({
  installations,
}: {
  installations: readonly PluginInstallation[] | undefined;
}) {
  const t = useT();
  const network = networkSignInInstallations(installations);
  if (network.length === 0) return null;
  return (
    <FieldGroup
      label={t("web.admin.settings.signIn.network.group")}
      description={t("web.admin.settings.signIn.network.description")}
    >
      {network.map((installation) => (
        <NetworkSignInRow key={installation.id} installation={installation} />
      ))}
    </FieldGroup>
  );
}

function NetworkSignInRow({ installation }: { installation: PluginInstallation }) {
  const t = useT();
  const updateBinding = useUpdateSignInBinding();
  const bindingWrites = useIsMutating({ mutationKey: signInBindingMutationKey });
  const [feedback, setFeedback] = useState<Feedback>(null);
  const feedbackRef = useRef<HTMLParagraphElement>(null);
  const enabledId = useId();
  const autoProvisionId = useId();
  const label = authProviderLabel(installation);
  const enabled = authBindingOf(installation)?.enabled === true;
  const autoProvision = savedAutoProvision(installation);
  const pending = bindingWrites > 0;

  function write(change: { enabled?: boolean; auto_provision?: boolean }, success: string) {
    updateBinding.mutate(
      {
        installationId: installation.id,
        body: signInBindingWrite(installation, { ...change, default_login: false }),
      },
      {
        onSuccess: () => setFeedback({ tone: "ok", text: success }),
        onError: (error) =>
          setFeedback({
            tone: "error",
            text: adminSignInErrorText(
              error,
              t("web.admin.settings.signIn.network.changeFailed", { name: label }),
            ),
          }),
      },
    );
  }

  return (
    <div className="settings-field-note">
      <FeedbackLine feedback={feedback} focusRef={feedbackRef} />
      <SettingFieldRow
        label={t("web.admin.settings.signIn.network.enableLabel", { name: label })}
        htmlFor={enabledId}
        description={t("web.admin.settings.signIn.network.enableDescription", { name: label })}
      >
        <Switch
          id={enabledId}
          checked={enabled}
          disabled={pending}
          onCheckedChange={(checked) =>
            write(
              { enabled: checked },
              checked
                ? t("web.admin.settings.signIn.network.turnedOn", { name: label })
                : t("web.admin.settings.signIn.network.turnedOff", { name: label }),
            )
          }
        />
      </SettingFieldRow>
      <SettingFieldRow
        label={t("web.admin.settings.signIn.network.autoProvisionLabel")}
        htmlFor={autoProvisionId}
        description={t("web.admin.settings.signIn.network.autoProvisionDescription", {
          name: label,
        })}
      >
        <Switch
          id={autoProvisionId}
          checked={autoProvision}
          disabled={pending}
          onCheckedChange={(checked) =>
            write(
              { auto_provision: checked },
              checked
                ? t("web.admin.settings.signIn.network.autoProvisionOn", { name: label })
                : t("web.admin.settings.signIn.network.autoProvisionOff", { name: label }),
            )
          }
        />
      </SettingFieldRow>
      <AdvancedSection
        id={`sign-in.network.${installation.plugin_id}`}
        title={t("web.admin.settings.signIn.network.howTitle")}
      >
        <div className="settings-field-note text-muted-foreground space-y-2 py-3.5 text-xs leading-relaxed">
          <p>
            {t("web.admin.settings.signIn.network.howWho", {
              name: label,
              capability: CAPABILITY,
              code,
            })}
          </p>
          <p>
            {t("web.admin.settings.signIn.network.howRoles", {
              capability: CAPABILITY,
              admin_role: ADMIN_ROLE,
              user_role: USER_ROLE,
              code,
            })}
          </p>
          <pre className="bg-muted overflow-x-auto rounded-md p-3 font-mono">{GRANT_EXAMPLE}</pre>
          <p>
            {t("web.admin.settings.signIn.network.howSharedTv", {
              name: label,
              link: (chunks) => (
                <Link
                  to={pluginPagePath(installation.plugin_id)}
                  className="text-foreground underline underline-offset-4"
                >
                  {chunks}
                </Link>
              ),
            })}
          </p>
        </div>
      </AdvancedSection>
    </div>
  );
}
