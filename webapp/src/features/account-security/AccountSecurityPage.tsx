import { useAsyncResource } from "../../app/AsyncResource";
import { AccessPageShell } from "../../components/AccessPageShell/AccessPageShell";
import { AccountSecurityContent } from "../../components/AccountSecurity/AccountSecurityContent";
import { SecuritySignOut } from "../../components/AccountSecurity/SecuritySignOut";
import { message } from "../../i18n/messages";
import {
  activateAuthenticator, beginAuthenticatorSetup, challengeSession, disableAuthenticator,
  regenerateRecoveryCodes, requestSecurityContext, signOutSecuritySession, type SecurityActions, type SecurityContextResult,
} from "./AccountSecurityApi";
import { useSignedInName } from "./SignedInName";

const actions: SecurityActions = {
  setup: beginAuthenticatorSetup, activate: activateAuthenticator, challenge: challengeSession,
  regenerate: regenerateRecoveryCodes, disable: disableAuthenticator, signOut: signOutSecuritySession,
};

export function AccountSecurityPage() {
  const resource = useAsyncResource<SecurityContextResult>(requestSecurityContext, { kind: "unavailable" });
  const ordinarySession = resource.value.kind === "ready" &&
    !resource.value.context.recoveryRequired && !resource.value.context.enrollmentRequired;
  const name = useSignedInName(ordinarySession);
  return (
    <AccessPageShell skipLabel={message("webapp.security.skip_to_main")}
      signedIn={name === undefined ? undefined : { name, signOut: <SecuritySignOut signOut={actions.signOut} /> }}>
      <AccountSecurityContent actions={actions} loading={resource.loading} state={resource.value} onRefresh={resource.retry} />
    </AccessPageShell>
  );
}
