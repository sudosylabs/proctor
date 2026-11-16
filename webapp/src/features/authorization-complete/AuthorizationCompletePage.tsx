import { useEffect, useRef, useState } from "react";

import { apiClient } from "../../api/client";
import { readProblemValue } from "../../api/problem";
import { useAsyncResource } from "../../app/AsyncResource";
import { AccessPageShell } from "../../components/AccessPageShell/AccessPageShell";
import { Button, ButtonLink } from "../../components/Button/Button";
import { TaskState, TaskStateActions, type TaskStatus } from "../../components/TaskState/TaskState";
import { message } from "../../i18n/messages";
import { requestSecurityContext } from "../account-security/AccountSecurityApi";
import styles from "./AuthorizationCompletePage.module.css";

type ConfirmationState = "checking" | "signed_in" | "no_session" | "unavailable" | "recovery_required" | "enrollment_required";

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function isCurrentUser(value: unknown): boolean {
  return (
    isRecord(value) &&
    typeof value.id === "string" &&
    value.id !== "" &&
    typeof value.username === "string" &&
    typeof value.display_name === "string"
  );
}

// Only a 200 from /users/me proves sign-in; reaching this path proves nothing.
// A recovery or enrollment restriction is considered only after the bounded
// invalid-token 401, never inferred from the URL or a bare rejection.
async function confirmSession(): Promise<ConfirmationState> {
  try {
    const { data, error, response } = await apiClient.GET("/api/v1/users/me");
    if (response.status === 200 && isCurrentUser(data)) {
      return "signed_in";
    }
    if (response.status === 401) {
      if (readProblemValue(error)?.code === "authentication.token_invalid") {
        const security = await requestSecurityContext();
        if (security.kind === "ready" && security.context.recoveryRequired) return "recovery_required";
        if (security.kind === "ready" && security.context.enrollmentRequired) return "enrollment_required";
      }
      return "no_session";
    }
    return "unavailable";
  } catch {
    return "unavailable";
  }
}

// Login and provider connection both end here and pass no input, so the page
// must never claim which flow finished or that a provider was connected.
export function AuthorizationCompletePage() {
  const confirmation = useAsyncResource<ConfirmationState>(
    confirmSession,
    "checking",
  );
  const state = confirmation.hasResolved ? confirmation.value : "checking";
  const retrying = confirmation.hasResolved && confirmation.loading;
  const [liveMessage, setLiveMessage] = useState(
    message("webapp.authorization_complete.checking.body"),
  );
  const retryRequested = useRef(false);
  const [focusUnavailable, setFocusUnavailable] = useState(false);

  useEffect(() => {
    if (!confirmation.loading && confirmation.hasResolved) {
      setLiveMessage(stateBody(confirmation.value));
      setFocusUnavailable(retryRequested.current && confirmation.value === "unavailable");
      retryRequested.current = false;
    }
  }, [confirmation.hasResolved, confirmation.loading, confirmation.value]);

  function retry() {
    if (confirmation.loading) {
      return;
    }
    retryRequested.current = true;
    setFocusUnavailable(false);
    setLiveMessage(message("webapp.authorization_complete.checking.body"));
    confirmation.retry();
  }

  const content = statusContent(state);
  return (
    <AccessPageShell skipLabel={message("webapp.authorization_complete.skip_to_main")}>
      <div className={styles.visuallyHidden} aria-live="polite" aria-atomic="true">
        {liveMessage}
      </div>
      <TaskState
        key={state}
        busy={state === "checking"}
        status={content.status}
        heading={content.heading}
        headingID="authorization-complete-heading"
        focusHeading={focusUnavailable}
        body={content.body}
      >
        <StatusActions state={state} retrying={retrying} onRetry={retry} />
      </TaskState>
    </AccessPageShell>
  );
}

function stateBody(state: ConfirmationState): string {
  return statusContent(state).body;
}

function StatusActions({
  onRetry,
  retrying,
  state,
}: {
  onRetry(): void;
  retrying: boolean;
  state: ConfirmationState;
}) {
  switch (state) {
    case "checking":
      return null;
    case "signed_in":
      return (
        <TaskStateActions>
          <ButtonLink href="/account/security">
            {message("webapp.authorization_complete.security")}
          </ButtonLink>
          <ButtonLink href="/account/connect-provider" variant="secondary">
            {message("webapp.authorization_complete.connect")}
          </ButtonLink>
        </TaskStateActions>
      );
    case "recovery_required":
      return (
        <TaskStateActions>
          <ButtonLink href="/account/security">
            {message("webapp.security.restore_access")}
          </ButtonLink>
        </TaskStateActions>
      );
    case "enrollment_required":
      return (
        <TaskStateActions>
          <ButtonLink href="/account/security">
            {message("webapp.security.enrollment_required.action")}
          </ButtonLink>
        </TaskStateActions>
      );
    case "no_session":
      return (
        <TaskStateActions>
          <ButtonLink href="/login">
            {message("webapp.authorization_complete.sign_in")}
          </ButtonLink>
        </TaskStateActions>
      );
    case "unavailable":
      return (
        <TaskStateActions>
          <Button
            isLoading={retrying}
            loadingLabel={message("webapp.authorization_complete.checking.heading")}
            onClick={onRetry}
          >
            {message("webapp.authorization_complete.unavailable.retry")}
          </Button>
          <ButtonLink href="/login" variant="secondary">
            {message("webapp.authorization_complete.sign_in")}
          </ButtonLink>
        </TaskStateActions>
      );
  }
}

function statusContent(state: ConfirmationState): {
  status?: TaskStatus;
  heading: string;
  body: string;
} {
  switch (state) {
    case "recovery_required":
      return {
        status: { label: message("webapp.reauthenticate.status.action_needed"), tone: "warning" },
        heading: message("webapp.security.restore_access"),
        body: message("webapp.security.recovery_required"),
      };
    case "enrollment_required":
      return {
        status: { label: message("webapp.reauthenticate.status.action_needed"), tone: "warning" },
        heading: message("webapp.security.enrollment_required.heading"),
        body: message("webapp.security.enrollment_required"),
      };
    case "checking":
      return {
        heading: message("webapp.authorization_complete.checking.heading"),
        body: message("webapp.authorization_complete.checking.body"),
      };
    case "signed_in":
      return {
        status: { label: message("webapp.authorization_complete.signed_in.label"), tone: "success" },
        heading: message("webapp.authorization_complete.signed_in.heading"),
        body: message("webapp.authorization_complete.signed_in.body"),
      };
    case "no_session":
      return {
        status: { label: message("webapp.authorization_complete.status.no_session"), tone: "warning" },
        heading: message("webapp.authorization_complete.no_session.heading"),
        body: message("webapp.authorization_complete.no_session.body"),
      };
    case "unavailable":
      return {
        status: { label: message("webapp.authorization_complete.status.unavailable"), tone: "warning" },
        heading: message("webapp.authorization_complete.unavailable.heading"),
        body: message("webapp.authorization_complete.unavailable.body"),
      };
  }
}
