import { useId, useRef, useState } from "react";
import { useIsMutating } from "@tanstack/react-query";
import { Link } from "react-router";

import type { PluginInstallation } from "@/api/types";
import { FeedbackLine, type Feedback } from "@/components/admin/FeedbackLine";
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

import { FieldGroup } from "./FieldGroup";
import { SettingFieldRow } from "./SettingField";

const GRANT_EXAMPLE = `"grants": [
  {"src": ["group:family-admins"], "dst": ["tag:silo"],
   "app": {"siloserver.org/cap/silo": [{"role": "admin"}]}},
  {"src": ["autogroup:member", "autogroup:shared"], "dst": ["tag:silo"],
   "app": {"siloserver.org/cap/silo": [{"role": "user"}]}}
]`;

/**
 * Settings → Sign-in: network sign-in plugins (such as Tailscale), which sign
 * in the owner of the device a request came from with no password. One can
 * be on beside the server's OIDC or LDAP provider. Turning it on or off and
 * account creation apply at once, like the provider slot's switch. Who may
 * sign in, and with which role, is the tailnet policy's (the plugin's "Who
 * can sign in" setting and siloserver.org/cap/silo grants); see
 * docs/architecture/external-sign-in.md#network-identity.
 */
export function NetworkSignInSection({
  installations,
}: {
  installations: readonly PluginInstallation[] | undefined;
}) {
  const network = networkSignInInstallations(installations);
  if (network.length === 0) return null;
  return (
    <FieldGroup
      label="Network sign-in"
      description="People whose device reaches this server over the network sign in as the device's owner, with no password. It can be on beside the provider above."
    >
      {network.map((installation) => (
        <NetworkSignInRow key={installation.id} installation={installation} />
      ))}
    </FieldGroup>
  );
}

function NetworkSignInRow({ installation }: { installation: PluginInstallation }) {
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
            text: adminSignInErrorText(error, `Couldn't change ${label}.`),
          }),
      },
    );
  }

  return (
    <div className="settings-field-note">
      <FeedbackLine feedback={feedback} focusRef={feedbackRef} />
      <SettingFieldRow
        label={`Sign in with ${label}`}
        htmlFor={enabledId}
        description={`Shows a "Continue as" button to people whose device reaches this server over ${label}.`}
      >
        <Switch
          id={enabledId}
          checked={enabled}
          disabled={pending}
          onCheckedChange={(checked) =>
            write(
              { enabled: checked },
              checked
                ? `${label} sign-in is on. Devices on ${label} can sign in now.`
                : `${label} sign-in is off. Accounts and their ${label} connections stay.`,
            )
          }
        />
      </SettingFieldRow>
      <SettingFieldRow
        label="Create accounts on first sign-in"
        htmlFor={autoProvisionId}
        description={`Off: only accounts already connected to ${label} can sign in.`}
      >
        <Switch
          id={autoProvisionId}
          checked={autoProvision}
          disabled={pending}
          onCheckedChange={(checked) =>
            write(
              { auto_provision: checked },
              checked
                ? `New ${label} users get a Silo account at first sign-in.`
                : `Only accounts already connected to ${label} can sign in.`,
            )
          }
        />
      </SettingFieldRow>
      <div className="text-muted-foreground space-y-2 py-3.5 text-xs leading-relaxed">
        <p>
          Anyone whose device can reach this server over {label} can sign in, including people you
          share the server with; tagged devices can&apos;t. To limit it, set the plugin&apos;s
          &ldquo;Who can sign in&rdquo; to people granted{" "}
          <code className="font-mono">siloserver.org/cap/silo</code>. {label} sign-in never matches
          an existing account by email: its owner connects {label} from their own Sign-in settings.
        </p>
        <p>
          Roles come from grants in your tailnet policy: a{" "}
          <code className="font-mono">siloserver.org/cap/silo</code> grant with{" "}
          <code className="font-mono">{`{"role":"admin"}`}</code> makes someone a Silo admin, and{" "}
          <code className="font-mono">{`{"role":"user"}`}</code> a regular user. Anyone who can edit
          the tailnet policy can make themselves a Silo admin. Without a grant, a person&apos;s role
          is left as it is, so removing someone&apos;s admin grant doesn&apos;t demote them: grant
          them <code className="font-mono">{`{"role":"user"}`}</code> instead. Policy changes apply
          at the next sign-in or access re-check. An account that also signs in through your main
          sign-in provider takes its role from that provider.
        </p>
        <pre className="bg-muted overflow-x-auto rounded-md p-3 font-mono">{GRANT_EXAMPLE}</pre>
        <p>
          Everyone using a shared TV signs in as whoever signed that TV in to {label}, with their
          role, so sign shared TVs in as someone without an admin grant; profiles keep their
          watching apart. Tag any device that relays other people&apos;s traffic to this server,
          such as a reverse proxy, or everyone behind it signs in as its owner.{" "}
          <Link
            to={pluginPagePath(installation.plugin_id)}
            className="text-foreground underline underline-offset-4"
          >
            {label} plugin settings
          </Link>
        </p>
      </div>
    </div>
  );
}
