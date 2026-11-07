import { useAsyncResource } from "../../app/AsyncResource";
import { AccessPageShell } from "../../components/AccessPageShell/AccessPageShell";
import { ReauthenticationContent } from "../../components/Reauthentication/ReauthenticationContent";
import { message } from "../../i18n/messages";
import { challengeSession, requestSecurityContext, type SecurityContextResult } from "../account-security/AccountSecurityApi";
import { useProviderName } from "../account-security/presentation";
import { useSignedInName } from "../account-security/SignedInName";
import { beginExternalProof, proveCurrentPassword, type ReauthenticationActions, type ReauthenticationTask } from "./ReauthenticationApi";

const actions: ReauthenticationActions = { password: proveCurrentPassword, external: beginExternalProof, challenge: challengeSession };

export function ReauthenticatePage({ task = "security", externalProofFailed = false }: {
  task?: ReauthenticationTask;
  externalProofFailed?: boolean;
}) {
  const resource = useAsyncResource<SecurityContextResult>(requestSecurityContext, { kind: "unavailable" });
  const context = resource.value.kind === "ready" ? resource.value.context : undefined;
  const name = useSignedInName(context !== undefined && !context.recoveryRequired);
  const providerName = useProviderName(context?.authenticationProviderID);
  return (
    <AccessPageShell skipLabel={message("webapp.security.skip_to_main")} signedIn={name === undefined ? undefined : { name }}>
      <ReauthenticationContent actions={actions} task={task} loading={resource.loading} state={resource.value}
        onRefresh={resource.retry} externalProofFailed={externalProofFailed} providerName={providerName} />
    </AccessPageShell>
  );
}
