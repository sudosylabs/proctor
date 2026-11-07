import { type FormEvent, useRef, useState } from "react";

import { useHostedInstitution } from "../../auth/HostedInstitution";
import { passwordRejectionMessage } from "../../auth/passwordRejection";
import type {
  InvitationAccountAcceptanceResult,
  InvitationAccountSubmission,
  InvitationSessionAcceptanceResult,
  InvitationStartResult,
  InvitationTransaction,
} from "../../features/join/InvitationApi";
import { formatDateTime } from "../../i18n/dateTime";
import { message, type MessageID } from "../../i18n/messages";
import { AccessTaskIntro } from "../AccessTaskIntro/AccessTaskIntro";
import { Button, ButtonLink } from "../Button/Button";
import { Fact, FactList } from "../FactList/FactList";
import { FormFeedback } from "../FormFeedback/FormFeedback";
import { InputField } from "../InputField/InputField";
import { PasswordField } from "../InputField/PasswordField";
import {
  TaskState,
  TaskStateActions,
  TaskStateAnnouncement,
} from "../TaskState/TaskState";
import styles from "./InvitationAcceptance.module.css";

type InvitationPurpose = InvitationTransaction["purpose"];
type InvitationRequirement = InvitationTransaction["requirement"];

const purposeLabels: Readonly<Record<InvitationPurpose, MessageID>> = {
  student_class: "webapp.join.facts.purpose.student_class",
  teacher_academic_unit: "webapp.join.facts.purpose.teacher_academic_unit",
  academic_unit_role: "webapp.join.facts.purpose.academic_unit_role",
  institution_role: "webapp.join.facts.purpose.institution_role",
};

const ledes: Readonly<
  Record<InvitationRequirement, Readonly<Record<InvitationPurpose, MessageID>>>
> = {
  account: {
    student_class: "webapp.join.lede.account.student_class",
    teacher_academic_unit: "webapp.join.lede.account.teacher_academic_unit",
    academic_unit_role: "webapp.join.lede.account.academic_unit_role",
    institution_role: "webapp.join.lede.account.institution_role",
  },
  session: {
    student_class: "webapp.join.lede.session.student_class",
    teacher_academic_unit: "webapp.join.lede.session.teacher_academic_unit",
    academic_unit_role: "webapp.join.lede.session.academic_unit_role",
    institution_role: "webapp.join.lede.session.institution_role",
  },
};

const acceptedBodies: Readonly<
  Record<InvitationRequirement, Readonly<Record<InvitationPurpose, MessageID>>>
> = {
  account: {
    student_class: "webapp.join.accepted.body.account.student_class",
    teacher_academic_unit:
      "webapp.join.accepted.body.account.teacher_academic_unit",
    academic_unit_role: "webapp.join.accepted.body.account.academic_unit_role",
    institution_role: "webapp.join.accepted.body.account.institution_role",
  },
  session: {
    student_class: "webapp.join.accepted.body.session.student_class",
    teacher_academic_unit:
      "webapp.join.accepted.body.session.teacher_academic_unit",
    academic_unit_role: "webapp.join.accepted.body.session.academic_unit_role",
    institution_role: "webapp.join.accepted.body.session.institution_role",
  },
};

const acceptedHeadings: Readonly<Record<InvitationRequirement, MessageID>> = {
  account: "webapp.join.accepted.heading.account",
  session: "webapp.join.accepted.heading.session",
};

export interface InvitationContentProps {
  acceptAccount(
    submission: InvitationAccountSubmission,
  ): Promise<InvitationAccountAcceptanceResult>;
  acceptSession(handle: string): Promise<InvitationSessionAcceptanceResult>;
  institutionName?: string;
  loading: boolean;
  onRetry(): void;
  state: InvitationStartResult;
}

export function InvitationContent({
  acceptAccount,
  acceptSession,
  institutionName,
  loading,
  onRetry,
  state,
}: InvitationContentProps) {
  const [accepted, setAccepted] = useState<InvitationTransaction>();
  const [retryRequested, setRetryRequested] = useState(false);
  const { supportAddress } = useHostedInstitution();

  function retry() {
    setRetryRequested(true);
    onRetry();
  }

  if (accepted !== undefined) {
    const heading = message(acceptedHeadings[accepted.requirement]);
    return (
      <>
        <TaskStateAnnouncement message={heading} />
        <InvitationRouteState
          focusHeading
          labelTone="success"
          heading={heading}
          body={message(
            acceptedBodies[accepted.requirement][accepted.purpose],
          )}
        >
          <TaskStateActions>
            <ButtonLink href="/login">
              {message("webapp.join.sign_in")}
            </ButtonLink>
          </TaskStateActions>
        </InvitationRouteState>
      </>
    );
  }
  if (loading) {
    return (
      <>
        <TaskStateAnnouncement message={message("webapp.join.loading.heading")} />
        <InvitationRouteState
          busy
          heading={message("webapp.join.loading.heading")}
          body={message("webapp.join.loading.body")}
        />
      </>
    );
  }
  if (state.kind === "invalid") {
    return (
      <>
        <TaskStateAnnouncement message={message("webapp.join.invalid.heading")} />
        <InvitationRouteState
          focusHeading={retryRequested}
          labelTone="danger"
          heading={message("webapp.join.invalid.heading")}
          body={message("webapp.join.invalid.body")}
        >
          <TaskStateActions>
            <ButtonLink href="/login">
              {message("webapp.join.sign_in")}
            </ButtonLink>
            {supportAddress === undefined ? null : (
              <ButtonLink href={`mailto:${supportAddress}`} variant="secondary">
                {message("webapp.join.invalid.contact_support")}
              </ButtonLink>
            )}
          </TaskStateActions>
        </InvitationRouteState>
      </>
    );
  }
  if (state.kind === "unavailable") {
    return (
      <>
        <TaskStateAnnouncement
          message={message("webapp.join.unavailable.heading")}
        />
        <InvitationRouteState
          focusHeading={retryRequested}
          labelTone="warning"
          heading={message("webapp.join.unavailable.heading")}
          body={message("webapp.join.unavailable.body")}
        >
          <TaskStateActions>
            <Button onClick={retry}>
              {message("webapp.join.unavailable.retry")}
            </Button>
          </TaskStateActions>
        </InvitationRouteState>
      </>
    );
  }
  const transaction = state.transaction;
  const onAccepted = () => setAccepted(transaction);
  return (
    <>
      <TaskStateAnnouncement message={invitationHeading(institutionName)} />
      <section className={styles.page} aria-labelledby="join-heading">
        <AccessTaskIntro
          kind={message("webapp.join.kind")}
          focusHeading={retryRequested}
          heading={invitationHeading(institutionName)}
          body={message(ledes[transaction.requirement][transaction.purpose])}
          headingID="join-heading"
        />
        <InvitationFacts transaction={transaction} />
        {transaction.requirement === "session" ? (
          <SessionInvitation
            handle={transaction.handle}
            acceptSession={acceptSession}
            onAccepted={onAccepted}
          />
        ) : (
          <InvitationAccountForm
            handle={transaction.handle}
            acceptAccount={acceptAccount}
            onAccepted={onAccepted}
          />
        )}
      </section>
    </>
  );
}

function invitationHeading(institutionName: string | undefined) {
  return institutionName === undefined
    ? message("webapp.join.heading_fallback")
    : message("webapp.join.heading", { Institution: institutionName });
}

function InvitationFacts({ transaction }: { transaction: InvitationTransaction }) {
  // The browser projection carries no Class, Academic Unit, or Role name yet.
  // When it does, that name is one more Fact between these two.
  return (
    <FactList aria-label={message("webapp.join.facts.label")}>
      <Fact label={message("webapp.join.facts.invited_as")}>
        {message(purposeLabels[transaction.purpose])}
      </Fact>
      <Fact label={message("webapp.join.facts.expires")}>
        <time dateTime={transaction.expires_at}>
          {formatDateTime(transaction.expires_at)}
        </time>
      </Fact>
    </FactList>
  );
}

function InvitationAccountForm({
  acceptAccount,
  handle,
  onAccepted,
}: {
  acceptAccount(
    submission: InvitationAccountSubmission,
  ): Promise<InvitationAccountAcceptanceResult>;
  handle: string;
  onAccepted(): void;
}) {
  const [firstName, setFirstName] = useState("");
  const [lastName, setLastName] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [usernameError, setUsernameError] = useState<string>();
  const [passwordError, setPasswordError] = useState<string>();
  const [formError, setFormError] = useState<string>();
  const [pending, setPending] = useState(false);
  const usernameRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) {
      return;
    }
    if (username.trim() === "") {
      setUsernameError(message("webapp.join.form.error.username_required"));
      usernameRef.current?.focus();
      return;
    }
    if (password === "") {
      setPasswordError(message("webapp.join.form.error.password_required"));
      passwordRef.current?.focus();
      return;
    }

    setPending(true);
    setFormError(undefined);
    const result = await acceptAccount({
      firstName,
      handle,
      lastName,
      password,
      username,
    });
    if (result.kind === "accepted") {
      setFirstName("");
      setLastName("");
      setUsername("");
      setPassword("");
      onAccepted();
    } else if (result.kind === "password_rejected") {
      setPasswordError(
        message(
          passwordRejectionMessage(
            result.reason,
            "webapp.join.form.error.password_invalid",
          ),
        ),
      );
      requestAnimationFrame(() => passwordRef.current?.focus());
    } else if (result.kind === "details_invalid") {
      setFormError(message("webapp.join.form.error.details_invalid"));
    } else if (result.kind === "rate_limited") {
      setFormError(message("webapp.join.form.error.rate_limited"));
    } else {
      setFormError(message("webapp.join.form.error.unavailable"));
    }
    setPending(false);
  }

  return (
    <form className={styles.form} onSubmit={submit} aria-busy={pending} noValidate>
      <InputField
        id="invitation-first-name"
        name="first_name"
        label={message("webapp.join.form.first_name")}
        autoComplete="given-name"
        value={firstName}
        onChange={(event) => {
          setFirstName(event.currentTarget.value);
          setFormError(undefined);
        }}
      />
      <InputField
        id="invitation-last-name"
        name="last_name"
        label={message("webapp.join.form.last_name")}
        autoComplete="family-name"
        value={lastName}
        onChange={(event) => {
          setLastName(event.currentTarget.value);
          setFormError(undefined);
        }}
      />
      <InputField
        ref={usernameRef}
        id="invitation-username"
        name="username"
        label={message("webapp.join.form.username")}
        autoCapitalize="none"
        autoComplete="username"
        spellCheck={false}
        value={username}
        errorMessage={usernameError}
        required
        onChange={(event) => {
          setUsername(event.currentTarget.value);
          setUsernameError(undefined);
          setFormError(undefined);
        }}
      />
      <PasswordField
        ref={passwordRef}
        id="invitation-password"
        name="password"
        label={message("webapp.join.form.password")}
        description={message("webapp.join.form.password_help")}
        autoComplete="new-password"
        value={password}
        errorMessage={passwordError}
        hidePasswordLabel={message("webapp.form.password_hide")}
        showPasswordLabel={message("webapp.form.password_show")}
        toggleDisabled={pending}
        required
        onChange={(event) => {
          setPassword(event.currentTarget.value);
          setPasswordError(undefined);
          setFormError(undefined);
        }}
      />
      <div className={styles.actions}>
        <Button
          type="submit"
          isLoading={pending}
          loadingLabel={message("webapp.join.form.submitting")}
        >
          {message("webapp.join.form.submit")}
        </Button>
      </div>
      <FormFeedback message={formError} />
    </form>
  );
}

function SessionInvitation({
  acceptSession,
  handle,
  onAccepted,
}: {
  acceptSession(handle: string): Promise<InvitationSessionAcceptanceResult>;
  handle: string;
  onAccepted(): void;
}) {
  const [pending, setPending] = useState(false);
  const [formError, setFormError] = useState<string>();
  const [needsSession, setNeedsSession] = useState(false);

  async function submit() {
    if (pending) {
      return;
    }
    setPending(true);
    setFormError(undefined);
    const result = await acceptSession(handle);
    if (result.kind === "accepted") {
      onAccepted();
    } else if (result.kind === "session_required") {
      setNeedsSession(true);
      setFormError(message("webapp.join.session.error.sign_in"));
    } else {
      setFormError(message("webapp.join.session.error.unavailable"));
    }
    setPending(false);
  }

  return (
    <div className={styles.form}>
      <div className={styles.actions}>
        <Button
          isLoading={pending}
          loadingLabel={message("webapp.join.session.submitting")}
          onClick={submit}
        >
          {message("webapp.join.session.submit")}
        </Button>
        {needsSession ? (
          <a
            aria-describedby="join-session-feedback"
            className={styles.signInLink}
            href="/login"
            rel="noopener noreferrer"
            target="_blank"
          >
            {message("webapp.join.sign_in")}
          </a>
        ) : null}
      </div>
      <FormFeedback id="join-session-feedback" message={formError} />
    </div>
  );
}

function InvitationRouteState({
  body,
  busy = false,
  children,
  focusHeading = false,
  heading,
  labelTone,
}: {
  body: string;
  busy?: boolean;
  children?: React.ReactNode;
  focusHeading?: boolean;
  heading: string;
  labelTone?: "success" | "warning" | "danger";
}) {
  return (
    <TaskState
      body={body}
      busy={busy}
      focusHeading={focusHeading}
      heading={heading}
      headingID="join-heading"
      label={message("webapp.join.kind")}
      labelTone={labelTone}
    >
      {children}
    </TaskState>
  );
}
