import { useEffect, useRef, useState } from "react";

import type { EmailVerificationResult } from "../../features/verify-email/VerifyEmailApi";
import { message, type MessageID } from "../../i18n/messages";
import { richMessage } from "../../i18n/richMessage";
import { Button, ButtonLink } from "../Button/Button";
import {
  TaskState,
  TaskStateActions,
  TaskStateAnnouncement,
} from "../TaskState/TaskState";
import styles from "./EmailVerificationContent.module.css";

type VerificationState = "ready" | "verified" | "invalid" | "unavailable";

export interface EmailVerificationContentProps {
  /** The address the token would confirm, once the read-only lookup names it. */
  address?: string;
  token?: string;
  verifyEmail(token: string): Promise<EmailVerificationResult>;
}

export function EmailVerificationContent({
  address,
  token,
  verifyEmail,
}: EmailVerificationContentProps) {
  const tokenRef = useRef(token);
  const activeRef = useRef(true);
  const [state, setState] = useState<VerificationState>(
    token === undefined ? "invalid" : "ready",
  );
  const [pending, setPending] = useState(false);
  const [transitioned, setTransitioned] = useState(false);
  const [liveMessage, setLiveMessage] = useState("");

  useEffect(() => {
    activeRef.current = true;
    return () => {
      activeRef.current = false;
    };
  }, []);

  async function submit() {
    const currentToken = tokenRef.current;
    if (pending || currentToken === undefined) {
      return;
    }

    setPending(true);
    setLiveMessage(message("webapp.verify_email.ready.pending_announcement"));
    const result = await verifyEmail(currentToken);
    if (!activeRef.current) {
      return;
    }

    const nextState = result.kind;
    if (nextState === "verified" || nextState === "invalid") {
      tokenRef.current = undefined;
    }
    setPending(false);
    setState(nextState);
    setTransitioned(true);
    setLiveMessage(message(copyFor(nextState).heading));
  }

  const copy = copyFor(state);
  const body =
    state === "ready" && address !== undefined
      ? richMessage("webapp.verify_email.ready.body_address", {
          Email: <strong translate="no">{address}</strong>,
        })
      : message(copy.body);

  return (
    <>
      <TaskStateAnnouncement message={liveMessage} />
      <TaskState
        // A new state remounts the heading so a completed action can focus it.
        key={state}
        body={body}
        focusHeading={transitioned}
        heading={message(copy.heading)}
        headingID="verify-email-heading"
        label={message("webapp.verify_email.kind")}
        labelTone={copy.tone}
      >
        <TaskStateActions className={styles.actions}>
          <VerificationActions
            pending={pending}
            state={state}
            onSubmit={submit}
          />
        </TaskStateActions>
      </TaskState>
    </>
  );
}

function VerificationActions({
  onSubmit,
  pending,
  state,
}: {
  onSubmit(): void;
  pending: boolean;
  state: VerificationState;
}) {
  switch (state) {
    case "ready":
    case "unavailable":
      return (
        <>
          <Button
            isLoading={pending}
            loadingLabel={message("webapp.verify_email.ready.pending")}
            onClick={onSubmit}
          >
            {message(
              state === "ready"
                ? "webapp.verify_email.ready.action"
                : "webapp.verify_email.unavailable.retry",
            )}
          </Button>
          <a className={styles.quietLink} href="/login">
            {message("webapp.verify_email.sign_in")}
          </a>
        </>
      );
    case "verified":
      return (
        <ButtonLink href="/login">
          {message("webapp.verify_email.verified.action")}
        </ButtonLink>
      );
    case "invalid":
      return (
        <ButtonLink href="/login">
          {message("webapp.verify_email.invalid.action")}
        </ButtonLink>
      );
  }
}

interface VerificationCopy {
  body: MessageID;
  heading: MessageID;
  tone?: "success" | "warning" | "danger";
}

function copyFor(state: VerificationState): VerificationCopy {
  switch (state) {
    case "ready":
      // When the read-only lookup names the address, the ready body is
      // replaced by its address-bearing variant.
      return {
        heading: "webapp.verify_email.ready.heading",
        body: "webapp.verify_email.ready.body",
      };
    case "verified":
      return {
        heading: "webapp.verify_email.verified.heading",
        body: "webapp.verify_email.verified.body",
        tone: "success",
      };
    case "invalid":
      return {
        heading: "webapp.verify_email.invalid.heading",
        body: "webapp.verify_email.invalid.body",
        tone: "danger",
      };
    case "unavailable":
      return {
        heading: "webapp.verify_email.unavailable.heading",
        body: "webapp.verify_email.unavailable.body",
        tone: "warning",
      };
  }
}
