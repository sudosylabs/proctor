import type { ReactNode } from "react";

import { useHostedInstitution } from "../../auth/HostedInstitution";
import type {
  RegistrationDiscoveryState,
  RegistrationSubmission,
  RegistrationSubmissionResult,
} from "../../features/register/RegistrationApi";
import { message } from "../../i18n/messages";
import {
  AccessTaskIntro,
  type AccessTaskKindTone,
} from "../AccessTaskIntro/AccessTaskIntro";
import { Button, ButtonLink } from "../Button/Button";
import { Loading } from "../Loading/Loading";
import { RegistrationForm, RegistrationIntro } from "./RegistrationForm";
import styles from "./Registration.module.css";

export interface RegistrationContentProps {
  accepted: boolean;
  onAccepted(): void;
  onPolicyChange(state: RegistrationDiscoveryState): void;
  onRetry(): void;
  state: RegistrationDiscoveryState;
  submitRegistration(
    submission: RegistrationSubmission,
  ): Promise<RegistrationSubmissionResult>;
}

export function RegistrationContent({
  accepted,
  onAccepted,
  onPolicyChange,
  onRetry,
  state,
  submitRegistration,
}: RegistrationContentProps) {
  const institution = useHostedInstitution();

  if (accepted) {
    return (
      <RegistrationState
        focusHeading
        kind={message("webapp.register.accepted.label")}
        kindTone="success"
        heading={message("webapp.register.accepted.heading")}
        body={message("webapp.register.accepted.body")}
      >
        <ButtonLink href="/login">
          {message("webapp.register.sign_in.action")}
        </ButtonLink>
      </RegistrationState>
    );
  }
  if (state.kind === "loading") {
    return (
      <section className={styles.page} aria-labelledby="register-heading">
        <RegistrationIntro />
        <div className={styles.loading}>
          <Loading label={message("webapp.register.loading")} showLabel size="large" />
        </div>
      </section>
    );
  }
  if (state.kind === "setup") {
    return (
      <RegistrationState
        heading={message("webapp.register.setup.heading")}
        body={message("webapp.register.setup.body")}
      >
        <ButtonLink href="/setup">
          {message("webapp.register.setup.open")}
        </ButtonLink>
      </RegistrationState>
    );
  }
  if (state.kind === "invitation_required") {
    return (
      <RegistrationState
        kindTone="warning"
        heading={message("webapp.register.invitation_required.heading")}
        body={
          institution.displayName === undefined
            ? message("webapp.register.invitation_required.body_unnamed")
            : message("webapp.register.invitation_required.body", {
                Institution: institution.displayName,
              })
        }
      >
        <ButtonLink href="/login">
          {message("webapp.register.sign_in.action")}
        </ButtonLink>
        {institution.supportAddress === undefined ? null : (
          <ButtonLink
            href={`mailto:${institution.supportAddress}`}
            variant="secondary"
          >
            {message("webapp.register.invitation_required.contact")}
          </ButtonLink>
        )}
      </RegistrationState>
    );
  }
  if (state.kind === "origin_mismatch") {
    return (
      <RegistrationState
        heading={message("webapp.register.origin_mismatch.heading")}
        body={message("webapp.register.origin_mismatch.body")}
      >
        <Button onClick={() => window.location.reload()}>
          {message("webapp.register.reload")}
        </Button>
      </RegistrationState>
    );
  }
  if (state.kind === "failure" || state.kind === "unavailable") {
    const unavailable = state.kind === "unavailable";
    return (
      <RegistrationState
        heading={message(
          unavailable
            ? "webapp.register.unavailable.heading"
            : "webapp.register.discovery_failure.heading",
        )}
        body={message(
          unavailable
            ? "webapp.register.unavailable.body"
            : "webapp.register.discovery_failure.body",
        )}
      >
        <Button onClick={onRetry}>
          {message("webapp.register.retry")}
        </Button>
      </RegistrationState>
    );
  }
  return (
    <RegistrationForm
      discovery={state.discovery}
      onAccepted={onAccepted}
      onPolicyChange={onPolicyChange}
      submitRegistration={submitRegistration}
    />
  );
}

function RegistrationState({
  body,
  children,
  focusHeading = false,
  heading,
  kind = message("webapp.register.kind"),
  kindTone,
}: {
  body: string;
  children: ReactNode;
  focusHeading?: boolean;
  heading: string;
  kind?: string;
  kindTone?: AccessTaskKindTone;
}) {
  return (
    <section className={styles.page} aria-labelledby="register-heading">
      <AccessTaskIntro
        body={body}
        focusHeading={focusHeading}
        heading={heading}
        headingID="register-heading"
        kind={kind}
        kindTone={kindTone}
      />
      <div className={styles.actions}>{children}</div>
    </section>
  );
}
