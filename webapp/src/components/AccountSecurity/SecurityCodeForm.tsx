import { useRef, useState, type FormEvent, type ReactNode } from "react";

import { message } from "../../i18n/messages";
import { AuthenticationCodeField, isCompleteAuthenticationCode, type AuthenticationCodeValue } from "../AuthenticationCodeField/AuthenticationCodeField";
import { Button } from "../Button/Button";
import { TaskStateActions } from "../TaskState/TaskState";
import styles from "./AccountSecurity.module.css";

export function SecurityCodeForm({ cancel, describedBy, deadline, onSubmit, pending, enrollment = false }: {
  /** Quiet escape placed after the submit action in the same row. */
  cancel?: ReactNode;
  /** ID of text that also describes the code field, such as a setup deadline. */
  describedBy?: string;
  /** Text shown with the field and referenced by `describedBy`. */
  deadline?: ReactNode;
  onSubmit(code: string): Promise<void>;
  pending: boolean;
  enrollment?: boolean;
}) {
  const [code, setCode] = useState<AuthenticationCodeValue>({ kind: "authenticator", code: "" });
  const [missing, setMissing] = useState(false);
  const input = useRef<HTMLInputElement>(null);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    if (!isCompleteAuthenticationCode(code)) {
      setMissing(true);
      input.current?.focus();
      return;
    }
    const submitted = code.code.trim();
    setCode({ ...code, code: "" });
    await onSubmit(submitted);
  }

  return (
    <form className={styles.stack} onSubmit={submit} aria-busy={pending} noValidate>
      <AuthenticationCodeField
        ref={input}
        id="security-code"
        name="code"
        allowRecovery={!enrollment}
        describedBy={describedBy}
        errorMessage={missing ? message(code.kind === "authenticator" ? "webapp.form.otp.incomplete" : "webapp.security.code.required") : undefined}
        value={code}
        disabled={pending}
        onChange={(value) => { setCode(value); setMissing(false); }}
      />
      {deadline === undefined ? null : <p className={styles.hint} id={describedBy}>{deadline}</p>}
      <TaskStateActions>
        <Button type="submit" isLoading={pending} loadingLabel={message("webapp.security.working")}>
          {message(enrollment ? "webapp.security.setup.activate" : "webapp.security.challenge.verify")}
        </Button>
        {cancel}
      </TaskStateActions>
    </form>
  );
}
