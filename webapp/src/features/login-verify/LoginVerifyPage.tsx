import type { ReactNode } from "react";

import { useHostedInstitution } from "../../auth/HostedInstitution";
import { AccessPageShell, RailItem, TaskRail } from "../../components/AccessPageShell/AccessPageShell";
import { SecurityCodeForm } from "../../components/AccountSecurity/SecurityCodeForm";
import { AccessTaskIntro } from "../../components/AccessTaskIntro/AccessTaskIntro";
import { Button, ButtonLink } from "../../components/Button/Button";
import { Fact, FactList } from "../../components/FactList/FactList";
import { FormFeedback } from "../../components/FormFeedback/FormFeedback";
import { TaskState, TaskStateActions, TaskStateAnnouncement, type TaskStatus } from "../../components/TaskState/TaskState";
import { formatDateTime } from "../../i18n/dateTime";
import { message } from "../../i18n/messages";
import type { ExternalSecondFactorContext } from "../external-second-factor/ExternalSecondFactorApi";
import {
  useLoginVerifyJourney,
  type LoginVerifyActions,
  type LoginVerifyFeedback,
  type LoginVerifySnapshot,
} from "./LoginVerifyJourney";
import styles from "./LoginVerifyPage.module.css";

const headingID = "login-verify-heading";

export function LoginVerifyPage() {
  const journey = useLoginVerifyJourney();
  return (
    <AccessPageShell skipLabel={message("webapp.login_verify.skip_to_main")}>
      <LoginVerifyContent snapshot={journey.snapshot} actions={journey} />
    </AccessPageShell>
  );
}

export function LoginVerifyContent({ actions, snapshot }: {
  actions: LoginVerifyActions;
  snapshot: LoginVerifySnapshot;
}) {
  const institution = useHostedInstitution().displayName;
  const { focusHeading, view } = snapshot;
  switch (view.kind) {
    case "checking":
    case "signing_in": {
      const heading = message(view.kind === "checking"
        ? "webapp.login_verify.checking.heading" : "webapp.login_verify.signing_in.heading");
      return <>
        <TaskStateAnnouncement message={heading} />
        <TaskState busy heading={heading} headingID={headingID} focusHeading={focusHeading}
          body={message(view.kind === "checking"
            ? "webapp.login_verify.checking.body" : "webapp.login_verify.signing_in.body")} />
      </>;
    }
    case "expired":
      return <Terminal focusHeading={focusHeading}
        status={{ label: message("webapp.login_verify.expired.status"), tone: "danger" }}
        heading={message("webapp.login_verify.expired.heading")}
        body={message("webapp.login_verify.expired.body")}>
        <TaskStateActions>
          <ButtonLink href="/login">{message("webapp.login_verify.expired.action")}</ButtonLink>
        </TaskStateActions>
      </Terminal>;
    case "mfa_unavailable":
      return <Terminal focusHeading={focusHeading}
        status={{ label: message("webapp.login_verify.mfa_unavailable.status"), tone: "danger" }}
        heading={message("webapp.login_verify.mfa_unavailable.heading")}
        body={institution === undefined
          ? message("webapp.login_verify.mfa_unavailable.body_unnamed")
          : message("webapp.login_verify.mfa_unavailable.body", { Institution: institution })}>
        <TaskStateActions>
          <ButtonLink href="/login" variant="secondary">{message("webapp.login_verify.mfa_unavailable.action")}</ButtonLink>
        </TaskStateActions>
      </Terminal>;
    case "unavailable":
      return <Terminal focusHeading={focusHeading}
        status={{ label: message("webapp.login_verify.unavailable.status"), tone: "warning" }}
        heading={message("webapp.login_verify.unavailable.heading")}
        body={message("webapp.login_verify.unavailable.body")}>
        <TaskStateActions>
          <Button onClick={actions.retry}>{message("webapp.login_verify.unavailable.retry")}</Button>
        </TaskStateActions>
      </Terminal>;
    case "code":
      return <CodeStep actions={actions} context={view.context} snapshot={snapshot} />;
  }
}

function Terminal({ body, children, focusHeading, heading, status }: {
  body: string;
  children?: ReactNode;
  focusHeading: boolean;
  heading: string;
  status: TaskStatus;
}) {
  return <>
    <TaskStateAnnouncement message={focusHeading ? heading : ""} />
    <TaskState heading={heading} headingID={headingID} focusHeading={focusHeading} body={body} status={status}>
      {children}
    </TaskState>
  </>;
}

function CodeStep({ actions, context, snapshot }: {
  actions: LoginVerifyActions;
  context: ExternalSecondFactorContext;
  snapshot: LoginVerifySnapshot;
}) {
  const { feedback, pending } = snapshot;
  return (
    <section className={styles.page} aria-labelledby={headingID}>
      <TaskStateAnnouncement message={pending ? message("webapp.second_factor.verifying") : ""} />
      <AccessTaskIntro headingID={headingID} focusHeading={snapshot.focusHeading}
        status={{ label: message("webapp.login_verify.status", { Provider: context.provider }), tone: "success" }}
        heading={message("webapp.login.mfa.heading")}
        body={message("webapp.login_verify.lede")} />
      <SecurityCodeForm pending={pending} onSubmit={actions.verify}
        errorMessage={feedback === "code_invalid" ? message("webapp.second_factor.code_invalid") : undefined}
        cancel={<a href="/login">{message("webapp.login_verify.cancel")}</a>} />
      <FormFeedback message={formMessage(feedback)} />
      <TaskRail>
        <RailItem heading={message("webapp.login_verify.rail.why.heading")}>
          <p>{message("webapp.login_verify.rail.why.body", { Provider: context.provider })}</p>
        </RailItem>
        <RailItem>
          <FactList>
            <Fact label={message("webapp.login_verify.rail.signed_in_with")}>{context.provider}</Fact>
            <Fact label={message("webapp.login_verify.rail.finish_by")}>
              <time dateTime={context.expiresAt}>{formatDateTime(context.expiresAt)}</time>
            </Fact>
          </FactList>
        </RailItem>
        <RailItem heading={message("webapp.login_verify.rail.lost.heading")}>
          <p>{message("webapp.login_verify.rail.lost.body")}</p>
        </RailItem>
      </TaskRail>
    </section>
  );
}

function formMessage(feedback: LoginVerifyFeedback | undefined): string | undefined {
  switch (feedback) {
    case "rate_limited": return message("webapp.desktop_reauthentication.rate_limited");
    case "unavailable": return message("webapp.login_verify.error.unavailable");
    default: return undefined;
  }
}
