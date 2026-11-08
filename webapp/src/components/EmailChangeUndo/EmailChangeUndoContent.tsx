import { useEffect, useRef, useState } from "react";

import type { EmailChangeUndoResult } from "../../features/undo-email-change/UndoEmailChangeApi";
import { formatDateTime } from "../../i18n/dateTime";
import { message, type MessageID } from "../../i18n/messages";
import { Button, ButtonLink } from "../Button/Button";
import { Fact, FactList } from "../FactList/FactList";
import { Notice } from "../Notice/Notice";
import {
  TaskState,
  TaskStateActions,
  TaskStateAnnouncement,
} from "../TaskState/TaskState";
import styles from "./EmailChangeUndoContent.module.css";

type UndoState =
  | "ready"
  | "restored"
  | "invalid"
  | "conflict"
  | "rate_limited"
  | "unavailable";

export interface EmailChangeUndoFacts {
  restoredEmail: string;
  maskedCurrentEmail: string;
  changedAt: string;
}

export interface EmailChangeUndoContentProps {
  /** The change the token would reverse, once the read-only lookup names it. */
  change?: EmailChangeUndoFacts;
  token?: string;
  undoEmailChange(token: string): Promise<EmailChangeUndoResult>;
}

export function EmailChangeUndoContent({
  change,
  token,
  undoEmailChange,
}: EmailChangeUndoContentProps) {
  const tokenRef = useRef(token);
  const activeRef = useRef(true);
  const [state, setState] = useState<UndoState>(
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
    setLiveMessage(message("webapp.undo_email_change.ready.pending_announcement"));
    const result = await undoEmailChange(currentToken);
    if (!activeRef.current) {
      return;
    }

    const nextState = result.kind;
    if (nextState === "restored" || nextState === "invalid" || nextState === "conflict") {
      tokenRef.current = undefined;
    }
    setPending(false);
    setState(nextState);
    setTransitioned(true);
    setLiveMessage(message(copyFor(nextState).heading));
  }

  const copy = copyFor(state);

  return (
    <>
      <TaskStateAnnouncement message={liveMessage} />
      <TaskState
        // A new state remounts the heading so a completed action can focus it.
        key={state}
        body={message(copy.body)}
        focusHeading={transitioned}
        heading={message(copy.heading)}
        headingID="undo-email-change-heading"
        label={message("webapp.undo_email_change.kind")}
        labelTone={copy.tone}
      >
        {change !== undefined && (state === "ready" || state === "rate_limited" || state === "unavailable") ? (
          <FactList className={styles.afterBody} aria-label={message("webapp.undo_email_change.facts.label")}>
            <Fact label={message("webapp.undo_email_change.facts.restores")}>
              <span translate="no">{change.restoredEmail}</span>
            </Fact>
            <Fact label={message("webapp.undo_email_change.facts.replaces")}>
              <span translate="no">{change.maskedCurrentEmail}</span>
            </Fact>
            <Fact label={message("webapp.undo_email_change.facts.changed")}>
              <time dateTime={change.changedAt}>{formatDateTime(change.changedAt)}</time>
            </Fact>
          </FactList>
        ) : null}
        {state === "restored" ? (
          <Notice
            className={styles.afterBody}
            lead={message("webapp.undo_email_change.restored.note_lead")}
            role="note"
          >
            {message("webapp.undo_email_change.restored.note")}
          </Notice>
        ) : null}
        <TaskStateActions className={styles.afterBody}>
          <UndoActions pending={pending} state={state} onSubmit={submit} />
        </TaskStateActions>
      </TaskState>
    </>
  );
}

function UndoActions({
  onSubmit,
  pending,
  state,
}: {
  onSubmit(): void;
  pending: boolean;
  state: UndoState;
}) {
  switch (state) {
    case "ready":
      return (
        <>
          <Button
            variant="danger"
            isLoading={pending}
            loadingLabel={message("webapp.undo_email_change.ready.pending")}
            onClick={onSubmit}
          >
            {message("webapp.undo_email_change.ready.action")}
          </Button>
          <ButtonLink href="/login" variant="secondary">
            {message("webapp.undo_email_change.ready.keep")}
          </ButtonLink>
        </>
      );
    case "rate_limited":
    case "unavailable":
      return (
        <>
          <Button
            variant="danger"
            isLoading={pending}
            loadingLabel={message("webapp.undo_email_change.ready.pending")}
            onClick={onSubmit}
          >
            {message("webapp.undo_email_change.unavailable.retry")}
          </Button>
          <SignInLink />
        </>
      );
    case "restored":
      return (
        <ButtonLink href="/login">
          {message("webapp.undo_email_change.restored.action")}
        </ButtonLink>
      );
    case "invalid":
    case "conflict":
      return <SignInLink />;
  }
}

function SignInLink() {
  return (
    <a className={styles.quietLink} href="/login">
      {message("webapp.undo_email_change.sign_in")}
    </a>
  );
}

interface UndoCopy {
  body: MessageID;
  heading: MessageID;
  tone: "success" | "warning" | "danger";
}

function copyFor(state: UndoState): UndoCopy {
  switch (state) {
    case "ready":
      // An unrecognised change to the account's address is itself the danger.
      return {
        heading: "webapp.undo_email_change.ready.heading",
        body: "webapp.undo_email_change.ready.body",
        tone: "danger",
      };
    case "restored":
      return {
        heading: "webapp.undo_email_change.restored.heading",
        body: "webapp.undo_email_change.restored.body",
        tone: "success",
      };
    case "invalid":
      return {
        heading: "webapp.undo_email_change.invalid.heading",
        body: "webapp.undo_email_change.invalid.body",
        tone: "danger",
      };
    case "conflict":
      return {
        heading: "webapp.undo_email_change.conflict.heading",
        body: "webapp.undo_email_change.conflict.body",
        tone: "danger",
      };
    case "rate_limited":
      return {
        heading: "webapp.undo_email_change.unavailable.heading",
        body: "webapp.undo_email_change.rate_limited.body",
        tone: "warning",
      };
    case "unavailable":
      return {
        heading: "webapp.undo_email_change.unavailable.heading",
        body: "webapp.undo_email_change.unavailable.body",
        tone: "warning",
      };
  }
}
