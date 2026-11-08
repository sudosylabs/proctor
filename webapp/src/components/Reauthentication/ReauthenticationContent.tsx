import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";

import type { SecurityContextResult } from "../../features/account-security/AccountSecurityApi";
import { formatMinutes, minutesUntil } from "../../features/account-security/presentation";
import { reauthenticationDestination, type ReauthenticationActions, type ReauthenticationFailure, type ReauthenticationTask } from "../../features/reauthenticate/ReauthenticationApi";
import { message } from "../../i18n/messages";
import { AccessTaskIntro } from "../AccessTaskIntro/AccessTaskIntro";
import { SecurityCodeForm } from "../AccountSecurity/SecurityCodeForm";
import { securityFailureMessage } from "../AccountSecurity/SecurityFeedback";
import { Button, ButtonLink } from "../Button/Button";
import { FormFeedback } from "../FormFeedback/FormFeedback";
import { PasswordField } from "../InputField/PasswordField";
import { Notice } from "../Notice/Notice";
import { TaskState, TaskStateActions, TaskStateAnnouncement } from "../TaskState/TaskState";
import styles from "./Reauthentication.module.css";

export function ReauthenticationContent({ actions, externalProofFailed, loading, onRefresh, providerName, state, task }: {
  actions: ReauthenticationActions;
  externalProofFailed: boolean;
  loading: boolean;
  onRefresh(): void;
  /** Display name of the Session's original external provider, when known. */
  providerName?: string;
  state: SecurityContextResult;
  task: ReauthenticationTask;
}) {
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<ReauthenticationFailure>();
  const [focusHeading, setFocusHeading] = useState(false);
  const [showExternalFailure, setShowExternalFailure] = useState(externalProofFailed);
  const [formGeneration, setFormGeneration] = useState(0);
  const viewGeneration = useRef(0);

  useEffect(() => {
    function discardSensitiveView() {
      viewGeneration.current += 1;
      setFormGeneration((current) => current + 1); setPending(false); setFailure(undefined);
    }
    function restore(event: PageTransitionEvent) {
      if (event.persisted) { discardSensitiveView(); onRefresh(); }
    }
    window.addEventListener("pagehide", discardSensitiveView);
    window.addEventListener("pageshow", restore);
    return () => {
      viewGeneration.current += 1;
      window.removeEventListener("pagehide", discardSensitiveView);
      window.removeEventListener("pageshow", restore);
    };
  }, [onRefresh]);

  function refresh() {
    setFailure(undefined); setFocusHeading(true); onRefresh();
  }

  function starting() { setPending(true); setFailure(undefined); setShowExternalFailure(false); }

  async function passwordProof(password: string) {
    if (pending) return;
    starting();
    const generation = viewGeneration.current;
    const result = await actions.password(password);
    if (generation !== viewGeneration.current) return;
    if (result.kind === "success") refresh(); else setFailure(result);
    setPending(false);
  }

  async function externalProof() {
    if (pending) return;
    starting();
    const generation = viewGeneration.current;
    const result = await actions.external(task);
    if (generation !== viewGeneration.current) return;
    if (result.kind === "redirect") { window.location.assign(result.url); return; }
    setFailure(result); setPending(false);
  }

  async function challenge(code: string) {
    if (pending) return;
    starting();
    const generation = viewGeneration.current;
    const result = await actions.challenge(code);
    if (generation !== viewGeneration.current) return;
    if (result.kind === "success") refresh(); else setFailure(result);
    setPending(false);
  }

  if (loading || state.kind !== "ready") {
    const noSession = !loading && state.kind === "no_session";
    const heading = message(loading ? "webapp.security.loading.heading" : noSession ? "webapp.security.no_session.heading" : "webapp.security.unavailable.heading");
    return <>
      <TaskStateAnnouncement message={heading} />
      <TaskState heading={heading} headingID="reauthentication-heading" focusHeading={focusHeading} busy={loading}
        label={message("webapp.reauthenticate.kind")}
        body={message(loading ? "webapp.security.loading.body" : noSession ? "webapp.security.no_session.body" : "webapp.security.unavailable.body")}>
        {!loading ? <TaskStateActions>{noSession
          ? <ButtonLink href="/login">{message("webapp.security.sign_in")}</ButtonLink>
          : <Button onClick={refresh}>{message("webapp.security.refresh")}</Button>}
        </TaskStateActions> : null}
      </TaskState>
    </>;
  }

  const context = state.context;
  const requiresRecovery = context.recoveryRequired && task === "connect-provider";
  const needsPrimary = !context.recentlyAuthenticated;
  const needsStrong = !context.recoveryRequired && context.authenticationStrength !== "multi_factor" && (context.enabled || task === "connect-provider");
  const complete = !requiresRecovery && !needsPrimary && !needsStrong;
  const step = requiresRecovery ? "recovery" : needsPrimary ? "primary" : needsStrong ? "strong" : "complete";
  // Before proof the lede states the configured window; once confirmed it
  // states what is actually left of this Session's window.
  const duration = formatMinutes(complete && context.recentlyAuthenticatedUntil !== undefined
    ? Math.max(1, minutesUntil(context.recentlyAuthenticatedUntil))
    : context.recentAuthenticationMinutes);
  const destination = reauthenticationDestination(task);
  const cancel = <a className={styles.cancel} href={destination}>{message("webapp.reauthenticate.cancel")}</a>;

  return (
    <section className={styles.page} aria-labelledby="reauthentication-heading">
      <TaskStateAnnouncement message={pending ? message("webapp.security.working") : ""} />
      <AccessTaskIntro key={step} headingID="reauthentication-heading" focusHeading={focusHeading}
        kind={message(requiresRecovery ? "webapp.security.eyebrow" : "webapp.reauthenticate.kind")}
        kindTone={requiresRecovery ? "warning" : complete ? "success" : undefined}
        heading={message(requiresRecovery ? "webapp.security.restore_access" : complete ? "webapp.reauthenticate.complete.heading" : "webapp.reauthenticate.heading")}
        body={stepBody(step, task, duration)} />
      {showExternalFailure ? <Notice tone="warning">{providerName === undefined
        ? message("webapp.reauthenticate.external.failed_unnamed")
        : message("webapp.reauthenticate.external.failed", { Provider: providerName })}</Notice> : null}
      {requiresRecovery ? <TaskStateActions>
        <ButtonLink href="/account/security">{message("webapp.security.restore_access")}</ButtonLink>
      </TaskStateActions> : needsPrimary ? <>
        {context.authenticationMethod === "password"
          ? <PasswordProofForm key={formGeneration} pending={pending} onSubmit={passwordProof} cancel={cancel} />
          : <>
            <Notice>{message("webapp.reauthenticate.external.help")}</Notice>
            <TaskStateActions>
              <Button isLoading={pending} loadingLabel={message("webapp.security.working")} onClick={externalProof}>
                {providerName === undefined
                  ? message("webapp.reauthenticate.external.action_unnamed")
                  : message("webapp.reauthenticate.external.action", { Provider: providerName })}
              </Button>
              {cancel}
            </TaskStateActions>
          </>}
      </> : needsStrong ? <>
        {context.enabled && context.serviceEnabled
          ? <SecurityCodeForm key={formGeneration} pending={pending} onSubmit={challenge} cancel={cancel} />
          : <>
            <Notice tone="warning">{message(context.serviceEnabled ? "webapp.reauthenticate.setup_required" : "webapp.security.service_disabled")}</Notice>
            <TaskStateActions>
              <ButtonLink href="/account/security">{message("webapp.reauthenticate.setup_required.action")}</ButtonLink>
              {cancel}
            </TaskStateActions>
          </>}
      </> : <TaskStateActions>
        <ButtonLink href={destination}>
          {message(task === "connect-provider" ? "webapp.reauthenticate.complete.connect" : "webapp.reauthenticate.complete.security")}
        </ButtonLink>
      </TaskStateActions>}
      <FormFeedback message={failure === undefined ? undefined : proofFailureMessage(failure)} />
      {failure !== undefined && ["method_required", "primary_required", "strong_required", "conflict", "disabled"].includes(failure.kind)
        ? <TaskStateActions><Button variant="secondary" onClick={refresh}>{message("webapp.security.refresh")}</Button></TaskStateActions> : null}
      {failure?.kind === "no_session" ? <TaskStateActions>
        <ButtonLink variant="secondary" href="/login">{message("webapp.security.sign_in")}</ButtonLink>
      </TaskStateActions> : null}
    </section>
  );
}

function stepBody(step: "recovery" | "primary" | "strong" | "complete", task: ReauthenticationTask, duration: string): string {
  switch (step) {
    case "recovery": return message("webapp.security.recovery_required");
    case "primary": return message(task === "connect-provider" ? "webapp.reauthenticate.lede.connect" : "webapp.reauthenticate.lede.security", { Duration: duration });
    case "strong": return message("webapp.reauthenticate.lede.strong");
    case "complete": return message("webapp.reauthenticate.complete.body", { Duration: duration });
  }
}

function PasswordProofForm({ cancel, onSubmit, pending }: { cancel: ReactNode; onSubmit(password: string): Promise<void>; pending: boolean }) {
  const [password, setPassword] = useState("");
  const [missing, setMissing] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    if (password === "") { setMissing(true); input.current?.focus(); return; }
    const submitted = password; setPassword(""); await onSubmit(submitted);
  }
  return <form className={styles.form} onSubmit={submit} aria-busy={pending} noValidate>
    <PasswordField ref={input} id="current-password" name="password" label={message("webapp.reauthenticate.password.label")}
      required disabled={pending} autoComplete="current-password" value={password}
      hidePasswordLabel={message("webapp.form.password_hide")} showPasswordLabel={message("webapp.form.password_show")}
      errorMessage={missing ? message("webapp.reauthenticate.password.required") : undefined}
      onChange={(event) => { setPassword(event.currentTarget.value); setMissing(false); }} />
    <TaskStateActions>
      <Button type="submit" isLoading={pending} loadingLabel={message("webapp.security.working")}>{message("webapp.reauthenticate.password.action")}</Button>
      {cancel}
    </TaskStateActions>
  </form>;
}

function proofFailureMessage(failure: ReauthenticationFailure): string {
  if (failure.kind === "invalid_password") return message("webapp.reauthenticate.password.invalid");
  if (failure.kind === "method_required") return message("webapp.reauthenticate.method_required");
  return securityFailureMessage(failure);
}
