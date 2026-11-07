import { useEffect, useState } from "react";

import type {
  BeginProviderConnectionResult,
  Provider,
  ProviderConnectionContextResult,
} from "../../features/connect-provider/ProviderConnectionApi";
import { message } from "../../i18n/messages";
import { AccessTaskIntro } from "../AccessTaskIntro/AccessTaskIntro";
import { Button, ButtonLink } from "../Button/Button";
import { FormFeedback } from "../FormFeedback/FormFeedback";
import { Notice } from "../Notice/Notice";
import {
  TaskState,
  TaskStateActions,
  TaskStateAnnouncement,
} from "../TaskState/TaskState";
import styles from "./ProviderConnection.module.css";

export interface ProviderConnectionContentProps {
  beginConnection(providerID: string): Promise<BeginProviderConnectionResult>;
  loading: boolean;
  onRedirect(url: string): void;
  onRetry(): void;
  state: ProviderConnectionContextResult;
}

export function ProviderConnectionContent({
  beginConnection,
  loading,
  onRedirect,
  onRetry,
  state,
}: ProviderConnectionContentProps) {
  const [retryRequested, setRetryRequested] = useState(false);

  function retry() {
    setRetryRequested(true);
    onRetry();
  }

  if (loading) {
    return (
      <>
        <TaskStateAnnouncement
          message={message("webapp.connect_provider.loading.heading")}
        />
        <ProviderRouteState
          busy
          heading={message("webapp.connect_provider.loading.heading")}
          body={message("webapp.connect_provider.loading.body")}
        />
      </>
    );
  }
  if (state.kind === "no_session") {
    return (
      <>
        <TaskStateAnnouncement
          message={message("webapp.connect_provider.no_session.heading")}
        />
        <ProviderRouteState
          focusHeading={retryRequested}
          heading={message("webapp.connect_provider.no_session.heading")}
          body={message("webapp.connect_provider.no_session.body")}
        >
          <TaskStateActions>
            <ButtonLink href="/login">
              {message("webapp.connect_provider.no_session.sign_in")}
            </ButtonLink>
          </TaskStateActions>
        </ProviderRouteState>
      </>
    );
  }
  if (state.kind === "unavailable") {
    return (
      <>
        <TaskStateAnnouncement
          message={message("webapp.connect_provider.unavailable.heading")}
        />
        <ProviderRouteState
          focusHeading={retryRequested}
          heading={message("webapp.connect_provider.unavailable.heading")}
          body={message("webapp.connect_provider.unavailable.body")}
        >
          <TaskStateActions>
            <Button onClick={retry}>
              {message("webapp.connect_provider.unavailable.retry")}
            </Button>
          </TaskStateActions>
        </ProviderRouteState>
      </>
    );
  }
  if (state.context.providers.length === 0) {
    return (
      <>
        <TaskStateAnnouncement
          message={message("webapp.connect_provider.empty.heading")}
        />
        <ProviderRouteState
          focusHeading={retryRequested}
          heading={message("webapp.connect_provider.empty.heading")}
          body={message("webapp.connect_provider.empty.body")}
        >
          <TaskStateActions>
            <a className={styles.returnLink} href="/account/security">
              {message("webapp.connect_provider.empty.return")}
            </a>
          </TaskStateActions>
        </ProviderRouteState>
      </>
    );
  }
  return (
    <>
      <TaskStateAnnouncement message={chooserHeading(state.context.providers)} />
      <ProviderChooser
        focusHeading={retryRequested}
        providers={state.context.providers}
        beginConnection={beginConnection}
        onRedirect={onRedirect}
      />
    </>
  );
}

function ProviderChooser({
  beginConnection,
  focusHeading,
  onRedirect,
  providers,
}: {
  beginConnection(providerID: string): Promise<BeginProviderConnectionResult>;
  focusHeading: boolean;
  onRedirect(url: string): void;
  providers: Provider[];
}) {
  const [selectedID, setSelectedID] = useState(providers[0]?.id ?? "");
  const [pending, setPending] = useState(false);
  const [feedback, setFeedback] = useState<string>();
  const [reauthenticationRequired, setReauthenticationRequired] = useState(false);
  const selected = providers.find((provider) => provider.id === selectedID);
  const single = providers.length === 1 ? providers[0] : undefined;
  const heading = chooserHeading(providers);

  useEffect(() => {
    if (!providers.some((provider) => provider.id === selectedID)) {
      setSelectedID(providers[0]?.id ?? "");
    }
  }, [providers, selectedID]);

  async function connect() {
    if (pending || selected === undefined) {
      return;
    }
    setPending(true);
    setFeedback(undefined);
    setReauthenticationRequired(false);
    const result = await beginConnection(selected.id);
    if (result.kind === "redirect") {
      onRedirect(result.url);
      return;
    }
    setFeedback(
      result.kind === "reauthentication_required"
        ? message("webapp.connect_provider.error.reauthentication")
        : message("webapp.connect_provider.error.unavailable", {
            Provider: selected.display_name,
          }),
    );
    setReauthenticationRequired(result.kind === "reauthentication_required");
    setPending(false);
  }

  return (
    <section className={styles.page} aria-labelledby="connect-provider-heading">
      <AccessTaskIntro
        kind={message("webapp.connect_provider.kind")}
        heading={heading}
        body={
          single === undefined
            ? message("webapp.connect_provider.multiple.body")
            : message("webapp.connect_provider.single.body", {
                Provider: single.display_name,
              })
        }
        focusHeading={focusHeading}
        headingID="connect-provider-heading"
      />
      {single === undefined ? (
        <fieldset className={styles.providerList}>
          <legend className={styles.visuallyHidden}>
            {message("webapp.connect_provider.group")}
          </legend>
          {providers.map((provider) => (
            <label
              key={provider.id}
              className={provider.id === selectedID ? styles.selected : undefined}
            >
              <input
                type="radio"
                name="provider"
                value={provider.id}
                checked={provider.id === selectedID}
                onChange={() => {
                  setSelectedID(provider.id);
                  setFeedback(undefined);
                  setReauthenticationRequired(false);
                }}
              />
              <span className={styles.providerName}>{provider.display_name}</span>
              <span className={styles.providerType} translate="no">
                {providerType(provider.type)}
              </span>
            </label>
          ))}
        </fieldset>
      ) : null}
      <Notice role="note">{message("webapp.connect_provider.note")}</Notice>
      <div className={styles.actionGroup}>
        <TaskStateActions>
          <Button
            isLoading={pending}
            loadingLabel={message("webapp.connect_provider.connecting")}
            onClick={connect}
          >
            {message("webapp.connect_provider.continue", {
              Provider: selected?.display_name ?? "",
            })}
          </Button>
          <a className={styles.returnLink} href="/account/security">
            {message("webapp.connect_provider.cancel")}
          </a>
        </TaskStateActions>
        <FormFeedback message={feedback} />
        {reauthenticationRequired ? (
          <a
            className={styles.returnLink}
            href="/account/reauthenticate?task=connect-provider"
          >
            {message("webapp.reauthenticate.action")}
          </a>
        ) : null}
      </div>
    </section>
  );
}

/** One provider needs no chooser, so the heading names it. */
function chooserHeading(providers: Provider[]): string {
  return providers.length === 1 && providers[0] !== undefined
    ? message("webapp.connect_provider.single.heading", {
        Provider: providers[0].display_name,
      })
    : message("webapp.connect_provider.multiple.heading");
}

function providerType(type: string): string {
  switch (type.toLowerCase()) {
    case "oidc":
      return message("webapp.connect_provider.type.oidc");
    case "cas":
      return message("webapp.connect_provider.type.cas");
    default:
      return message("webapp.connect_provider.type.external");
  }
}

function ProviderRouteState({
  body,
  busy = false,
  children,
  focusHeading = false,
  heading,
}: {
  body: string;
  busy?: boolean;
  children?: React.ReactNode;
  focusHeading?: boolean;
  heading: string;
}) {
  return (
    <TaskState
      body={body}
      busy={busy}
      focusHeading={focusHeading}
      heading={heading}
      headingID="connect-provider-heading"
      label={message("webapp.connect_provider.kind")}
    >
      {children}
    </TaskState>
  );
}
