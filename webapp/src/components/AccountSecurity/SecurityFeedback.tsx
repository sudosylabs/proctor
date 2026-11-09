import type { SecurityFailure } from "../../features/account-security/AccountSecurityApi";
import { message } from "../../i18n/messages";
import { ButtonLink } from "../Button/Button";
import { FormFeedback } from "../FormFeedback/FormFeedback";
import styles from "./AccountSecurity.module.css";

export function securityFailureMessage(failure: SecurityFailure): string {
  switch (failure.kind) {
    case "no_session": return message("webapp.security.error.no_session");
    case "primary_required": return message("webapp.security.error.primary_required");
    case "strong_required": return message("webapp.security.error.strong_required");
    case "invalid_code": return message("webapp.security.error.invalid_code");
    case "rate_limited": return message("webapp.security.error.rate_limited");
    case "disabled": return message("webapp.security.error.disabled");
    case "conflict": return message("webapp.security.error.conflict");
    case "required_by_policy": return message("webapp.security.error.required_by_policy");
    case "unavailable": return message("webapp.security.error.unavailable");
  }
}

export function SecurityFeedback({ failure, task = "security" }: {
  failure?: SecurityFailure;
  task?: "security" | "connect-provider";
}) {
  return (
    <>
      <FormFeedback message={failure === undefined ? undefined : securityFailureMessage(failure)} />
      {failure?.kind === "primary_required" || failure?.kind === "strong_required" ? (
        <ButtonLink className={styles.followUp} variant="secondary" href={`/account/reauthenticate?task=${task}`}>
          {message("webapp.reauthenticate.action")}
        </ButtonLink>
      ) : null}
      {failure?.kind === "no_session" ? (
        <ButtonLink className={styles.followUp} variant="secondary" href="/login">{message("webapp.security.sign_in")}</ButtonLink>
      ) : null}
    </>
  );
}
