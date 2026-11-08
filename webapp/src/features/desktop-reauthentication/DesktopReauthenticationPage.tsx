import { useRef, useState, type FormEvent, type ReactNode } from "react";

import { AccessPageShell } from "../../components/AccessPageShell/AccessPageShell";
import { AccessTaskIntro } from "../../components/AccessTaskIntro/AccessTaskIntro";
import { Button } from "../../components/Button/Button";
import { FormFeedback } from "../../components/FormFeedback/FormFeedback";
import { PasswordField } from "../../components/InputField/PasswordField";
import { Notice } from "../../components/Notice/Notice";
import { TaskState, TaskStateActions, TaskStateAnnouncement } from "../../components/TaskState/TaskState";
import { message } from "../../i18n/messages";
import type { DesktopReauthenticationContext, DesktopReauthenticationProof } from "./DesktopReauthenticationApi";
import {
  useDesktopReauthenticationJourney,
  type DesktopReauthenticationActions,
  type DesktopReauthenticationFeedback,
  type DesktopReauthenticationSnapshot,
} from "./DesktopReauthenticationJourney";
import styles from "./DesktopReauthenticationPage.module.css";

export interface DesktopReauthenticationPageProps {
  proof?: DesktopReauthenticationProof;
  state?: string;
  providerFailed?: boolean;
}

export function DesktopReauthenticationPage({ proof, providerFailed, state }: DesktopReauthenticationPageProps) {
  const journey = useDesktopReauthenticationJourney({ proof, state, providerFailed });
  return (
    <AccessPageShell skipLabel={message("webapp.desktop_reauthentication.skip_to_main")}>
      <DesktopReauthenticationContent snapshot={journey.snapshot} actions={journey} />
    </AccessPageShell>
  );
}

const headingID = "desktop-reauthentication-heading";

export function DesktopReauthenticationContent({ actions, snapshot }: {
  actions: DesktopReauthenticationActions;
  snapshot: DesktopReauthenticationSnapshot;
}) {
  const { focusHeading, view } = snapshot;
  switch (view.kind) {
    case "checking":
    case "returning": {
      const heading = message(view.kind === "checking"
        ? "webapp.desktop_reauthentication.checking.heading" : "webapp.desktop_reauthentication.returning.heading");
      return <>
        <TaskStateAnnouncement message={heading} />
        <TaskState busy heading={heading} headingID={headingID} label={message("webapp.desktop_reauthentication.kind")}
          body={message(view.kind === "checking"
            ? "webapp.desktop_reauthentication.checking.body" : "webapp.desktop_reauthentication.approved.returning")} />
      </>;
    }
    case "approved":
      return <Terminal focusHeading={focusHeading} tone="success"
        heading={message("webapp.desktop_reauthentication.approved.heading")}
        body={message("webapp.desktop_reauthentication.approved.body")} />;
    case "canceled":
      return <Terminal focusHeading={focusHeading}
        heading={message("webapp.desktop_reauthentication.canceled.heading")}
        body={message("webapp.desktop_reauthentication.canceled.body")} />;
    case "invalid":
      return <Terminal focusHeading={focusHeading} tone="danger"
        heading={message("webapp.desktop_reauthentication.invalid.heading")}
        body={message("webapp.desktop_reauthentication.invalid.body")} />;
    case "unavailable":
      return <Terminal focusHeading={focusHeading} tone="warning"
        heading={message("webapp.desktop_reauthentication.unavailable.heading")}
        body={message("webapp.desktop_reauthentication.unavailable.body")}>
        <TaskStateActions>
          <Button onClick={actions.retry}>{message("webapp.desktop_reauthentication.unavailable.retry")}</Button>
        </TaskStateActions>
      </Terminal>;
    case "proof":
      return <Proof actions={actions} context={view.context} snapshot={snapshot} />;
  }
}

function Terminal({ body, children, focusHeading, heading, tone }: {
  body: string;
  children?: ReactNode;
  focusHeading: boolean;
  heading: string;
  tone?: "success" | "warning" | "danger";
}) {
  return <>
    <TaskStateAnnouncement message={focusHeading ? heading : ""} />
    <TaskState heading={heading} headingID={headingID} focusHeading={focusHeading} body={body}
      label={message("webapp.desktop_reauthentication.kind")} labelTone={tone}>
      {children}
    </TaskState>
  </>;
}

function Proof({ actions, context, snapshot }: {
  actions: DesktopReauthenticationActions;
  context: DesktopReauthenticationContext;
  snapshot: DesktopReauthenticationSnapshot;
}) {
  const { pending } = snapshot;
  const provider = context.provider;
  const cancel = <Button variant="text" disabled={pending !== undefined && pending !== "cancel"}
    isLoading={pending === "cancel"} loadingLabel={message("webapp.desktop_reauthentication.working")}
    onClick={() => { void actions.cancel(); }}>
    {message("webapp.desktop_reauthentication.cancel")}
  </Button>;
  return (
    <section className={styles.page} aria-labelledby={headingID}>
      <TaskStateAnnouncement message={pending === undefined ? "" : message("webapp.desktop_reauthentication.working")} />
      <AccessTaskIntro headingID={headingID} focusHeading={snapshot.focusHeading}
        kind={message("webapp.desktop_reauthentication.kind")}
        heading={message("webapp.desktop_reauthentication.heading")}
        body={provider === undefined
          ? message("webapp.desktop_reauthentication.lede.password", { Name: context.name })
          : message("webapp.desktop_reauthentication.lede.provider", { Name: context.name, Provider: provider.display_name })} />
      {snapshot.providerFailed && provider !== undefined
        ? <Notice tone="warning">{message("webapp.desktop_reauthentication.provider.failed", { Provider: provider.display_name })}</Notice>
        : null}
      {provider === undefined
        ? <PasswordForm pending={pending} feedback={snapshot.feedback} onSubmit={actions.provePassword} cancel={cancel} />
        : <>
          <TaskStateActions>
            <Button isLoading={pending === "provider"} disabled={pending !== undefined && pending !== "provider"}
              loadingLabel={message("webapp.desktop_reauthentication.working")} onClick={actions.chooseProvider}>
              {message("webapp.desktop_reauthentication.provider.action", { Provider: provider.display_name })}
            </Button>
            {cancel}
          </TaskStateActions>
          <FormFeedback message={feedbackMessage(snapshot.feedback)} />
        </>}
      <p className={styles.note}>{message("webapp.desktop_reauthentication.note")}</p>
    </section>
  );
}

function PasswordForm({ cancel, feedback, onSubmit, pending }: {
  cancel: ReactNode;
  feedback?: DesktopReauthenticationFeedback;
  onSubmit(password: string): Promise<void>;
  pending: DesktopReauthenticationSnapshot["pending"];
}) {
  const [password, setPassword] = useState("");
  const [missing, setMissing] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending !== undefined) return;
    if (password === "") { setMissing(true); input.current?.focus(); return; }
    const submitted = password;
    setPassword("");
    await onSubmit(submitted);
  }
  return <form className={styles.form} onSubmit={(event) => { void submit(event); }} aria-busy={pending === "password"} noValidate>
    <PasswordField ref={input} id="desktop-reauthentication-password" name="password"
      label={message("webapp.desktop_reauthentication.password.label")} required disabled={pending !== undefined}
      autoComplete="current-password" value={password}
      hidePasswordLabel={message("webapp.form.password_hide")} showPasswordLabel={message("webapp.form.password_show")}
      errorMessage={missing ? message("webapp.desktop_reauthentication.password.required") : undefined}
      onChange={(event) => { setPassword(event.currentTarget.value); setMissing(false); }} />
    <TaskStateActions>
      <Button type="submit" isLoading={pending === "password"} disabled={pending !== undefined && pending !== "password"}
        loadingLabel={message("webapp.desktop_reauthentication.working")}>
        {message("webapp.desktop_reauthentication.password.action")}
      </Button>
      {cancel}
    </TaskStateActions>
    <FormFeedback message={feedbackMessage(feedback)} />
  </form>;
}

function feedbackMessage(feedback: DesktopReauthenticationFeedback | undefined): string | undefined {
  switch (feedback) {
    case "invalid_password": return message("webapp.desktop_reauthentication.password.invalid");
    case "rate_limited": return message("webapp.desktop_reauthentication.rate_limited");
    case "unavailable": return message("webapp.desktop_reauthentication.error.unavailable");
    default: return undefined;
  }
}
