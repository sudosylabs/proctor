import type { ReactNode } from "react";

import type {
  SetupCreated,
  SetupStatus,
  SetupSubmission,
  SetupSubmissionResult,
} from "../../features/setup/SetupApi";
import { message } from "../../i18n/messages";
import {
  AccessTaskIntro,
  type AccessTaskKindTone,
} from "../AccessTaskIntro/AccessTaskIntro";
import { Button, ButtonLink } from "../Button/Button";
import { Fact, FactList } from "../FactList/FactList";
import { Loading } from "../Loading/Loading";
import { SetupForm, SetupIntro } from "./SetupForm";
import styles from "./Setup.module.css";

export interface SetupContentProps {
  /** What this page just created; absent when setup already existed. */
  created?: SetupCreated;
  onComplete(created?: SetupCreated): void;
  onRetry(): void;
  status: SetupStatus;
  submitSetup(submission: SetupSubmission): Promise<SetupSubmissionResult>;
}

export function SetupContent({
  created,
  onComplete,
  onRetry,
  status,
  submitSetup,
}: SetupContentProps) {
  if (status.kind === "loading") {
    return (
      <section className={styles.page} aria-labelledby="setup-heading">
        <SetupIntro />
        <div className={styles.loading}>
          <Loading label={message("webapp.setup.loading")} showLabel size="large" />
        </div>
      </section>
    );
  }
  if (status.kind === "failure") {
    return (
      <SetupState
        heading={message("webapp.setup.status_failure.heading")}
        body={message("webapp.setup.status_failure.body")}
      >
        <div className={styles.actions}>
          <Button onClick={onRetry}>
            {message("webapp.setup.status_failure.retry")}
          </Button>
        </div>
      </SetupState>
    );
  }
  if (status.kind === "complete" && created !== undefined) {
    return (
      <SetupState
        focusHeading
        kindTone="success"
        heading={message("webapp.setup.created.heading", {
          Institution: created.institutionDisplayName,
        })}
        body={message("webapp.setup.created.body")}
      >
        <FactList aria-label={message("webapp.setup.created.facts")}>
          <Fact label={message("webapp.setup.created.institution")}>
            {created.institutionDisplayName}
          </Fact>
          <Fact label={message("webapp.setup.created.administrator")}>
            {message("webapp.setup.created.administrator_value", {
              Name: created.administratorName,
              Email: created.administratorEmail,
            })}
          </Fact>
        </FactList>
        <div className={styles.actions}>
          <ButtonLink href="/login">
            {message("webapp.setup.sign_in")}
          </ButtonLink>
        </div>
      </SetupState>
    );
  }
  if (status.kind === "complete") {
    return (
      <SetupState
        heading={message("webapp.setup.already_initialized.heading")}
        body={message("webapp.setup.already_initialized.body")}
      >
        <div className={styles.actions}>
          <ButtonLink href="/login">
            {message("webapp.setup.sign_in")}
          </ButtonLink>
        </div>
      </SetupState>
    );
  }
  return <SetupForm onComplete={onComplete} submitSetup={submitSetup} />;
}

function SetupState({
  body,
  children,
  focusHeading = false,
  heading,
  kindTone,
}: {
  body: string;
  children: ReactNode;
  focusHeading?: boolean;
  heading: string;
  kindTone?: AccessTaskKindTone;
}) {
  return (
    <section className={styles.page} aria-labelledby="setup-heading">
      <AccessTaskIntro
        body={body}
        focusHeading={focusHeading}
        heading={heading}
        headingID="setup-heading"
        kind={message("webapp.setup.kind")}
        kindTone={kindTone}
      />
      {children}
    </section>
  );
}
