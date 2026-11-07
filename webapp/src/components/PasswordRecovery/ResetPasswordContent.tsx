import { type FormEvent, useRef, useState } from "react";
import { formatPasswordLength, usePasswordMinimumLength } from "../../auth/passwordPolicy";

import type { PasswordResetCompletionResult } from "../../features/reset-password/ResetPasswordApi";
import { message } from "../../i18n/messages";
import { AccessTaskIntro } from "../AccessTaskIntro/AccessTaskIntro";
import { Button, ButtonLink } from "../Button/Button";
import { FormFeedback } from "../FormFeedback/FormFeedback";
import { PasswordField } from "../InputField/PasswordField";
import {
  TaskState,
  TaskStateActions,
  TaskStateAnnouncement,
} from "../TaskState/TaskState";
import styles from "./PasswordRecovery.module.css";
import { passwordRejectionMessage } from "../../auth/passwordRejection";

type ResetState = "ready" | "complete" | "invalid";

export interface ResetPasswordContentProps {
  completeReset(
    token: string,
    password: string,
  ): Promise<PasswordResetCompletionResult>;
  token?: string;
}

export function ResetPasswordContent({
  completeReset,
  token,
}: ResetPasswordContentProps) {
  const minimumPasswordLength = usePasswordMinimumLength();
  const tokenRef = useRef(token);
  const [state, setState] = useState<ResetState>(
    token === undefined ? "invalid" : "ready",
  );
  const [transitioned, setTransitioned] = useState(false);
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [passwordError, setPasswordError] = useState<string>();
  const [confirmationError, setConfirmationError] = useState<string>();
  const [formError, setFormError] = useState<string>();
  const [pending, setPending] = useState(false);
  const passwordRef = useRef<HTMLInputElement>(null);
  const confirmationRef = useRef<HTMLInputElement>(null);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const currentToken = tokenRef.current;
    if (pending || currentToken === undefined) {
      return;
    }
    if (password === "") {
      setPasswordError(message("webapp.reset_password.form.error.password_required"));
      passwordRef.current?.focus();
      return;
    }
    if (confirmation === "") {
      setConfirmationError(
        message("webapp.reset_password.form.error.confirmation_required"),
      );
      confirmationRef.current?.focus();
      return;
    }
    if (confirmation !== password) {
      setConfirmationError(message("webapp.reset_password.form.error.mismatch"));
      confirmationRef.current?.focus();
      return;
    }

    setPending(true);
    setPasswordError(undefined);
    setConfirmationError(undefined);
    setFormError(undefined);
    const result = await completeReset(currentToken, password);
    if (result.kind === "complete") {
      tokenRef.current = undefined;
      setPassword("");
      setConfirmation("");
      setState("complete");
      setTransitioned(true);
    } else if (result.kind === "invalid") {
      tokenRef.current = undefined;
      setPassword("");
      setConfirmation("");
      setState("invalid");
      setTransitioned(true);
    } else if (result.kind === "password_rejected") {
      setPasswordError(
        message(
          passwordRejectionMessage(
            result.reason,
            "webapp.reset_password.form.error.password_invalid",
          ),
        ),
      );
      requestAnimationFrame(() => passwordRef.current?.focus());
    } else if (result.kind === "rate_limited") {
      setFormError(message("webapp.reset_password.form.error.rate_limited"));
    } else {
      setFormError(message("webapp.reset_password.form.error.unavailable"));
    }
    setPending(false);
  }

  if (state !== "ready") {
    const complete = state === "complete";
    const heading = message(
      complete
        ? "webapp.reset_password.complete.heading"
        : "webapp.reset_password.invalid.heading",
    );
    return (
      <>
        <TaskStateAnnouncement message={transitioned ? heading : ""} />
        <TaskState
          body={message(
            complete
              ? "webapp.reset_password.complete.body"
              : "webapp.reset_password.invalid.body",
          )}
          focusHeading={transitioned}
          heading={heading}
          headingID="reset-heading"
          label={message("webapp.reset_password.kind")}
          labelTone={complete ? "success" : "danger"}
        >
          <TaskStateActions className={styles.afterBody}>
            {complete ? (
              <ButtonLink href="/login">
                {message("webapp.reset_password.complete.action")}
              </ButtonLink>
            ) : (
              <>
                <ButtonLink href="/account/forgot-password">
                  {message("webapp.reset_password.invalid.action")}
                </ButtonLink>
                <a className={styles.quietLink} href="/login">
                  {message("webapp.reset_password.sign_in")}
                </a>
              </>
            )}
          </TaskStateActions>
        </TaskState>
      </>
    );
  }

  return (
    <>
      <TaskStateAnnouncement message="" />
      <section className={styles.page} aria-labelledby="reset-heading">
        <AccessTaskIntro
          kind={message("webapp.reset_password.kind")}
          heading={message("webapp.reset_password.context.heading")}
          body={message("webapp.reset_password.context.body")}
          headingID="reset-heading"
        />
        <form
          className={styles.form}
          onSubmit={submit}
          aria-busy={pending}
          noValidate
        >
          <PasswordField
            ref={passwordRef}
            id="new-password"
            name="new_password"
            label={message("webapp.reset_password.form.password")}
            description={minimumPasswordLength === undefined
            ? message("webapp.reset_password.form.password_help_unsized")
            : message("webapp.reset_password.form.password_help", { Count: formatPasswordLength(minimumPasswordLength) })}
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
          <PasswordField
            ref={confirmationRef}
            id="confirm-new-password"
            name="confirm_new_password"
            label={message("webapp.reset_password.form.confirmation")}
            autoComplete="new-password"
            value={confirmation}
            errorMessage={confirmationError}
            hidePasswordLabel={message("webapp.form.password_hide")}
            showPasswordLabel={message("webapp.form.password_show")}
            toggleDisabled={pending}
            required
            onChange={(event) => {
              setConfirmation(event.currentTarget.value);
              setConfirmationError(undefined);
              setFormError(undefined);
            }}
          />
          <div className={styles.actionRow}>
            <Button
              type="submit"
              isLoading={pending}
              loadingLabel={message("webapp.reset_password.form.submitting")}
            >
              {message("webapp.reset_password.form.submit")}
            </Button>
          </div>
          <FormFeedback message={formError} />
        </form>
      </section>
    </>
  );
}
