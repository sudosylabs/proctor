import { type FormEvent, type ReactNode, useEffect, useRef, useState } from "react";

import type { PasswordResetRequestResult } from "../../features/forgot-password/ForgotPasswordApi";
import { message } from "../../i18n/messages";
import { AccessTaskIntro } from "../AccessTaskIntro/AccessTaskIntro";
import { Button } from "../Button/Button";
import { FormFeedback } from "../FormFeedback/FormFeedback";
import { InputField } from "../InputField/InputField";
import { Notice } from "../Notice/Notice";
import {
  TaskState,
  TaskStateActions,
  TaskStateAnnouncement,
} from "../TaskState/TaskState";
import styles from "./PasswordRecovery.module.css";

export interface ForgotPasswordContentProps {
  requestReset(email: string): Promise<PasswordResetRequestResult>;
}

export function ForgotPasswordContent({
  requestReset,
}: ForgotPasswordContentProps) {
  const [email, setEmail] = useState("");
  const [emailError, setEmailError] = useState<string>();
  const [formError, setFormError] = useState<string>();
  const [pending, setPending] = useState(false);
  // The address the person typed, echoed only from component memory.
  const [acceptedEmail, setAcceptedEmail] = useState<string>();
  const [returnedToForm, setReturnedToForm] = useState(false);
  const emailRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (returnedToForm) {
      emailRef.current?.focus();
    }
  }, [returnedToForm]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) {
      return;
    }
    if (email.trim() === "") {
      setEmailError(message("webapp.forgot_password.form.error.email_required"));
      emailRef.current?.focus();
      return;
    }
    if (emailRef.current?.validity.typeMismatch) {
      setEmailError(message("webapp.forgot_password.form.error.email_invalid"));
      emailRef.current.focus();
      return;
    }

    setPending(true);
    setEmailError(undefined);
    setFormError(undefined);
    const result = await requestReset(email);
    if (result.kind === "accepted") {
      setAcceptedEmail(email.trim());
      setReturnedToForm(false);
    } else if (result.kind === "rate_limited") {
      setFormError(message("webapp.forgot_password.form.error.rate_limited"));
    } else {
      setFormError(message("webapp.forgot_password.form.error.unavailable"));
    }
    setPending(false);
  }

  if (acceptedEmail !== undefined) {
    return (
      <>
        <TaskStateAnnouncement
          message={message("webapp.forgot_password.accepted.heading")}
        />
        <TaskState
          body={withPlaceholder(
            message("webapp.forgot_password.accepted.body"),
            "{{.Email}}",
            <strong className={styles.address}>{acceptedEmail}</strong>,
          )}
          focusHeading
          heading={message("webapp.forgot_password.accepted.heading")}
          headingID="forgot-heading"
          label={message("webapp.forgot_password.kind")}
          labelTone="success"
        >
          <div className={styles.outcome}>
            <Notice
              lead={message("webapp.forgot_password.accepted.note_lead")}
              role="note"
            >
              {withPlaceholder(
                message("webapp.forgot_password.accepted.note"),
                "{{.Action}}",
                <button
                  className={styles.inlineAction}
                  type="button"
                  onClick={() => {
                    setAcceptedEmail(undefined);
                    setReturnedToForm(true);
                  }}
                >
                  {message("webapp.forgot_password.accepted.resend")}
                </button>,
              )}
            </Notice>
            <TaskStateActions>
              <a className={styles.quietLink} href="/login">
                {message("webapp.forgot_password.sign_in")}
              </a>
            </TaskStateActions>
          </div>
        </TaskState>
      </>
    );
  }

  return (
    <>
      <TaskStateAnnouncement message="" />
      <section className={styles.page} aria-labelledby="forgot-heading">
        <AccessTaskIntro
          kind={message("webapp.forgot_password.kind")}
          heading={message("webapp.forgot_password.context.heading")}
          body={message("webapp.forgot_password.context.body")}
          headingID="forgot-heading"
        />
        <form
          className={styles.form}
          onSubmit={submit}
          aria-busy={pending}
          noValidate
        >
          <InputField
            ref={emailRef}
            id="password-reset-email"
            name="email"
            label={message("webapp.forgot_password.form.email")}
            type="email"
            inputMode="email"
            autoCapitalize="none"
            autoComplete="email"
            spellCheck={false}
            value={email}
            errorMessage={emailError}
            required
            onChange={(event) => {
              setEmail(event.currentTarget.value);
              setEmailError(undefined);
              setFormError(undefined);
            }}
          />
          <div className={styles.actionRow}>
            <Button
              type="submit"
              isLoading={pending}
              loadingLabel={message("webapp.forgot_password.form.submitting")}
            >
              {message("webapp.forgot_password.form.submit")}
            </Button>
            <a className={styles.quietLink} href="/login">
              {message("webapp.forgot_password.sign_in")}
            </a>
          </div>
          <FormFeedback message={formError} />
        </form>
      </section>
    </>
  );
}

/** Places a rendered node where a translation names its placeholder. */
function withPlaceholder(template: string, placeholder: string, node: ReactNode) {
  const index = template.indexOf(placeholder);
  if (index < 0) {
    return template;
  }
  return (
    <>
      {template.slice(0, index)}
      {node}
      {template.slice(index + placeholder.length)}
    </>
  );
}
