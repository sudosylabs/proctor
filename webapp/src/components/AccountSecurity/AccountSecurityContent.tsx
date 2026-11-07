import { useEffect, useRef, useState, type ReactNode } from "react";

import type {
  AuthenticatorSetup, SecurityActions, SecurityContext, SecurityContextResult, SecurityFailure,
} from "../../features/account-security/AccountSecurityApi";
import { formatMinutes, minutesUntil, useProviderName } from "../../features/account-security/presentation";
import { message } from "../../i18n/messages";
import { AccessTaskIntro, type AccessTaskKindTone } from "../AccessTaskIntro/AccessTaskIntro";
import { Button, ButtonLink } from "../Button/Button";
import { Fact, FactList } from "../FactList/FactList";
import { Notice } from "../Notice/Notice";
import { TaskState, TaskStateActions, TaskStateAnnouncement } from "../TaskState/TaskState";
import { RecoveryCodes } from "./RecoveryCodes";
import { AuthenticatorSetup as AuthenticatorSetupContent } from "./AuthenticatorSetup";
import { SecurityCodeForm } from "./SecurityCodeForm";
import { SecurityFeedback } from "./SecurityFeedback";
import { SecuritySignOut } from "./SecuritySignOut";
import styles from "./AccountSecurity.module.css";

type Task =
  | { kind: "status" }
  | { kind: "setup"; setup: AuthenticatorSetup }
  | { kind: "codes"; codes: string[] }
  | { kind: "challenge" }
  | { kind: "confirm_disable" }
  | { kind: "confirm_regenerate" }
  | { kind: "uncertain" };

export function AccountSecurityContent({ actions, loading, onRefresh, state }: {
  actions: SecurityActions;
  loading: boolean;
  onRefresh(): void;
  state: SecurityContextResult;
}) {
  const [task, setTask] = useState<Task>({ kind: "status" });
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<SecurityFailure>();
  const [focusHeading, setFocusHeading] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  const viewGeneration = useRef(0);
  const providerName = useProviderName(state.kind === "ready" ? state.context.authenticationProviderID : undefined);

  useEffect(() => {
    function discardSensitiveView() {
      viewGeneration.current += 1;
      setTask({ kind: "status" }); setFailure(undefined); setPending(false);
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

  function changeTask(next: Task) {
    setFailure(undefined);
    setFocusHeading(true);
    setTask(next);
    setAnnouncement("");
  }

  function refresh(successMessage = "") {
    changeTask({ kind: "status" });
    setAnnouncement(successMessage);
    onRefresh();
  }

  function failed(result: SecurityFailure, uncertainMutation = false) {
    if (uncertainMutation && result.kind === "unavailable") {
      changeTask({ kind: "uncertain" });
    } else {
      setFailure(result);
    }
  }

  async function setup() {
    if (pending) return;
    setPending(true); setFailure(undefined);
    const generation = viewGeneration.current;
    const result = await actions.setup();
    if (generation !== viewGeneration.current) return;
    if (result.kind === "success") changeTask({ kind: "setup", setup: result.setup });
    else failed(result, true);
    setPending(false);
  }

  async function activate(code: string) {
    if (pending) return;
    setPending(true); setFailure(undefined);
    const generation = viewGeneration.current;
    const result = await actions.activate(code);
    if (generation !== viewGeneration.current) return;
    if (result.kind === "success") changeTask({ kind: "codes", codes: result.codes });
    else failed(result, true);
    setPending(false);
  }

  async function challenge(code: string) {
    if (pending) return;
    setPending(true); setFailure(undefined);
    const generation = viewGeneration.current;
    const result = await actions.challenge(code);
    if (generation !== viewGeneration.current) return;
    if (result.kind === "success") refresh(message("webapp.security.challenge.success"));
    else failed(result);
    setPending(false);
  }

  async function regenerate() {
    if (pending) return;
    setPending(true); setFailure(undefined);
    const generation = viewGeneration.current;
    const result = await actions.regenerate();
    if (generation !== viewGeneration.current) return;
    if (result.kind === "success") changeTask({ kind: "codes", codes: result.codes });
    else failed(result, true);
    setPending(false);
  }

  async function disable() {
    if (pending) return;
    setPending(true); setFailure(undefined);
    const generation = viewGeneration.current;
    const result = await actions.disable();
    if (generation !== viewGeneration.current) return;
    if (result.kind === "success") refresh(message("webapp.security.disable.success"));
    else failed(result, true);
    setPending(false);
  }

  if (loading || (state.kind !== "ready" && task.kind === "status")) {
    const noSession = !loading && state.kind === "no_session";
    const heading = message(loading ? "webapp.security.loading.heading" : noSession ? "webapp.security.no_session.heading" : "webapp.security.unavailable.heading");
    return <>
      <TaskStateAnnouncement message={heading} />
      <TaskState heading={heading} headingID="security-heading" focusHeading={focusHeading} busy={loading}
        label={message("webapp.security.eyebrow")}
        body={message(loading ? "webapp.security.loading.body" : noSession ? "webapp.security.no_session.body" : "webapp.security.unavailable.body")}>
        {!loading ? <TaskStateActions>{noSession
          ? <ButtonLink href="/login">{message("webapp.security.sign_in")}</ButtonLink>
          : <Button onClick={() => refresh()}>{message("webapp.security.refresh")}</Button>}
        </TaskStateActions> : null}
      </TaskState>
    </>;
  }

  const context = state.kind === "ready" ? state.context : undefined;
  const content = taskCopy(task);
  const feedback = <>
    <SecurityFeedback failure={failure} />
    {failure?.kind === "conflict" || failure?.kind === "disabled"
      ? <TaskStateActions><Button variant="secondary" onClick={() => refresh()}>{message("webapp.security.refresh")}</Button></TaskStateActions>
      : null}
  </>;
  const cancel = <Button variant="text" disabled={pending} onClick={() => refresh()}>{message("webapp.security.cancel")}</Button>;

  return (
    <section className={styles.page} aria-labelledby="security-heading">
      <TaskStateAnnouncement message={pending ? message("webapp.security.working") : announcement} />
      <AccessTaskIntro key={task.kind} headingID="security-heading" focusHeading={focusHeading}
        kind={content.kind} kindTone={content.tone} heading={content.heading} body={content.body} />
      {task.kind === "status" && context !== undefined ? <StatusSummary context={context} providerName={providerName}
        verify={<Button variant="text" disabled={pending} onClick={() => changeTask({ kind: "challenge" })}>
          {message("webapp.security.challenge.action")}
        </Button>} /> : null}
      {task.kind === "status" && context !== undefined ? <StatusSections context={context} pending={pending} feedback={feedback}
        onSetup={setup} onRefresh={() => refresh()} signOut={actions.signOut}
        onRegenerate={() => changeTask({ kind: "confirm_regenerate" })}
        onDisable={() => changeTask({ kind: "confirm_disable" })} /> : null}
      {task.kind === "setup" ? <>
        <AuthenticatorSetupContent setup={task.setup} />
        <Notice>{message("webapp.security.setup.private")}</Notice>
        <SecurityCodeForm pending={pending} onSubmit={activate} enrollment cancel={cancel}
          describedBy="authenticator-setup-expiry" deadline={setupDeadline(task.setup.expiresAt)} />
        {feedback}
      </> : null}
      {task.kind === "codes" ? <RecoveryCodes codes={task.codes} onDone={() => refresh(message("webapp.security.codes.saved"))} /> : null}
      {task.kind === "challenge" ? <>
        <SecurityCodeForm pending={pending} onSubmit={challenge} cancel={cancel} />
        {feedback}
      </> : null}
      {task.kind === "confirm_disable" ? <>
        <Notice>{message("webapp.security.disable.warning")}</Notice>
        <TaskStateActions>
          <Button variant="danger" isLoading={pending} loadingLabel={message("webapp.security.working")} onClick={disable}>{message("webapp.security.disable.confirm")}</Button>
          <Button variant="secondary" disabled={pending} onClick={() => refresh()}>{message("webapp.security.disable.keep")}</Button>
        </TaskStateActions>
        {feedback}
      </> : null}
      {task.kind === "confirm_regenerate" ? <>
        <TaskStateActions>
          <Button isLoading={pending} loadingLabel={message("webapp.security.working")} onClick={regenerate}>{message("webapp.security.regenerate.confirm")}</Button>
          {cancel}
        </TaskStateActions>
        {feedback}
      </> : null}
      {task.kind === "uncertain" ? <>
        <Notice tone="warning">{message("webapp.security.uncertain.guidance")}</Notice>
        <TaskStateActions><Button onClick={() => refresh()}>{message("webapp.security.refresh")}</Button></TaskStateActions>
      </> : null}
    </section>
  );
}

function setupDeadline(expiresAt: string): string {
  const minutes = minutesUntil(expiresAt);
  return minutes === 0
    ? message("webapp.security.setup.expired")
    : message("webapp.security.setup.expires", { Duration: formatMinutes(minutes) });
}

/** The current state of this account and Session, one hairline fact per row. */
function StatusSummary({ context, providerName, verify }: {
  context: SecurityContext;
  providerName?: string;
  verify: ReactNode;
}) {
  const strong = context.authenticationStrength === "multi_factor";
  const canVerify = context.enabled && context.serviceEnabled && !context.recoveryRequired && !strong;
  return (
    <FactList>
      {/* The status response has no password-change time yet. When it does,
          a "Password" fact with that date belongs first in this list. */}
      <Fact label={message("webapp.security.status.two_step")}>
        <StateValue tone={context.enabled ? "success" : context.pending ? "warning" : "off"}>
          {message(context.enabled ? "webapp.security.status.on" : context.pending ? "webapp.security.status.pending" : "webapp.security.status.off")}
        </StateValue>
      </Fact>
      {context.enabled ? <Fact label={message("webapp.security.status.codes")}>
        {context.recoveryCodesRemaining > 0
          ? message("webapp.security.status.codes_left", { Count: new Intl.NumberFormat().format(context.recoveryCodesRemaining) })
          : <StateValue tone="warning">{message("webapp.security.status.codes_none")}</StateValue>}
      </Fact> : null}
      <Fact label={message("webapp.security.status.session")}>
        <span className={styles.sessionValue}>
          <span>{sessionDescription(context, providerName)}</span>
          {canVerify ? verify : null}
        </span>
      </Fact>
    </FactList>
  );
}

function sessionDescription(context: SecurityContext, providerName?: string): string {
  if (context.authenticationStrength === "multi_factor") return message("webapp.security.status.session_strong");
  if (context.authenticationMethod === "password") return message("webapp.security.status.session_password");
  return providerName === undefined
    ? message("webapp.security.status.session_external_unnamed")
    : message("webapp.security.status.session_external", { Provider: providerName });
}

function StateValue({ children, tone }: { children: ReactNode; tone: "success" | "warning" | "off" }) {
  return <span className={`${styles.state} ${styles[tone]}`} data-proctor-state={tone}>
    <span className={styles.stateMarker} aria-hidden="true" />{children}
  </span>;
}

/** One decision per section: its own small heading, sentence, and action. */
function SecuritySection({ body, children, heading, id }: { body: ReactNode; children?: ReactNode; heading: ReactNode; id: string }) {
  return (
    <section className={styles.section} aria-labelledby={id}>
      <h2 id={id}>{heading}</h2>
      <p>{body}</p>
      {children}
    </section>
  );
}

function StatusSections({ context, feedback, onDisable, onRefresh, onRegenerate, onSetup, pending, signOut }: {
  context: SecurityContext;
  feedback: ReactNode;
  onDisable(): void;
  onRefresh(): void;
  onRegenerate(): void;
  onSetup(): void;
  pending: boolean;
  signOut: SecurityActions["signOut"];
}) {
  const setupAction = <TaskStateActions>
    <Button isLoading={pending} loadingLabel={message("webapp.security.working")} onClick={onSetup}>
      {message(context.pending ? "webapp.security.setup.restart" : "webapp.security.setup.action")}
    </Button>
  </TaskStateActions>;

  if (context.recoveryRequired) {
    // A restricted Session has no confirmed name for the footer, so its
    // sign-out lives here, next to the only other things it can do.
    return <>
      <Notice tone="warning" lead={message("webapp.security.recovery_required.lead")}>
        {message("webapp.security.recovery_required")}
      </Notice>
      {context.serviceEnabled ? <>
        {context.pending ? <Notice>{message("webapp.security.setup.pending")}</Notice> : null}
        {setupAction}
        {feedback}
        <TaskStateActions><SecuritySignOut signOut={signOut} /></TaskStateActions>
      </> : <>
        <Notice>{message("webapp.security.recovery_unavailable")}</Notice>
        <TaskStateActions>
          <Button variant="secondary" onClick={onRefresh}>{message("webapp.security.refresh")}</Button>
          <SecuritySignOut signOut={signOut} variant="secondary" />
        </TaskStateActions>
      </>}
    </>;
  }
  if (!context.serviceEnabled) {
    return <Notice tone="warning">{message("webapp.security.service_disabled")}</Notice>;
  }
  if (!context.enabled) {
    return <SecuritySection id="security-setup-heading" heading={message("webapp.security.setup.section.heading")}
      body={message("webapp.security.setup.section.body")}>
      {context.pending ? <Notice>{message("webapp.security.setup.pending")}</Notice> : null}
      {setupAction}
      {feedback}
    </SecuritySection>;
  }
  return <>
    <SecuritySection id="security-codes-heading" heading={message("webapp.security.regenerate.section.heading")}
      body={message("webapp.security.regenerate.section.body")}>
      {context.recoveryCodesRemaining === 0 ? <Notice tone="warning">{message("webapp.security.status.no_codes")}</Notice> : null}
      <TaskStateActions>
        <Button variant="secondary" disabled={pending} onClick={onRegenerate}>{message("webapp.security.regenerate.action")}</Button>
      </TaskStateActions>
    </SecuritySection>
    <SecuritySection id="security-disable-heading" heading={message("webapp.security.disable.section.heading")}
      body={message("webapp.security.disable.section.body")}>
      <TaskStateActions>
        <Button variant="secondary" disabled={pending} onClick={onDisable}>{message("webapp.security.disable.action")}</Button>
      </TaskStateActions>
    </SecuritySection>
  </>;
}

function taskCopy(task: Task): { kind: string; tone?: AccessTaskKindTone; heading: string; body: string } {
  const twoStep = message("webapp.security.kind.two_step");
  switch (task.kind) {
    case "status": return { kind: message("webapp.security.eyebrow"), heading: message("webapp.security.heading"), body: message("webapp.security.body") };
    case "setup": return { kind: twoStep, heading: message("webapp.security.setup.heading"), body: message("webapp.security.setup.body") };
    case "codes": return { kind: twoStep, tone: "success", heading: message("webapp.security.codes.heading"), body: message("webapp.security.codes.body") };
    case "challenge": return { kind: twoStep, heading: message("webapp.security.challenge.heading"), body: message("webapp.security.challenge.body") };
    case "confirm_disable": return { kind: twoStep, tone: "danger", heading: message("webapp.security.disable.heading"), body: message("webapp.security.disable.body") };
    case "confirm_regenerate": return { kind: message("webapp.security.regenerate.section.heading"), heading: message("webapp.security.regenerate.heading"), body: message("webapp.security.regenerate.body") };
    case "uncertain": return { kind: message("webapp.security.eyebrow"), heading: message("webapp.security.uncertain.heading"), body: message("webapp.security.uncertain.body") };
  }
}
