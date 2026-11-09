import {
  type FormEvent,
  type ReactNode,
  useEffect,
  useRef,
  useState,
} from "react";

import { useAsyncResource } from "../../app/AsyncResource";
import { useHostedInstitution } from "../../auth/HostedInstitution";
import { navigateToProvider } from "../../auth/navigation";
import type { PublicAccessDiscovery } from "../../auth/PublicAccessDiscovery";
import { AccessPageShell } from "../../components/AccessPageShell/AccessPageShell";
import { AccessTaskIntro } from "../../components/AccessTaskIntro/AccessTaskIntro";
import { AuthenticationCodeField, isCompleteAuthenticationCode, type AuthenticationCodeValue } from "../../components/AuthenticationCodeField/AuthenticationCodeField";
import { Button, ButtonLink } from "../../components/Button/Button";
import { FormFeedback } from "../../components/FormFeedback/FormFeedback";
import { InputField } from "../../components/InputField/InputField";
import { PasswordField } from "../../components/InputField/PasswordField";
import { Loading } from "../../components/Loading/Loading";
import { Notice } from "../../components/Notice/Notice";
import { TaskHeading } from "../../components/TaskState/TaskState";
import { message } from "../../i18n/messages";
import { richMessage } from "../../i18n/richMessage";
import { authenticateLocal, type AuthenticateLocal } from "./LoginApi";
import {
  requestLoginDiscovery,
  type LoginDiscoveryState,
} from "./LoginDiscovery";
import styles from "./LoginPage.module.css";

type FieldErrors = Partial<Record<"login_id" | "password" | "mfa_code", string>>;

export interface LoginPageProps {
  externalLoginFailed: boolean;
}

export function LoginPage({ externalLoginFailed }: LoginPageProps) {
  const discoveryResource = useAsyncResource<LoginDiscoveryState>(
    () => requestLoginDiscovery(window.location.origin),
    { kind: "loading" },
  );
  const discoveryState: LoginDiscoveryState = discoveryResource.loading
    ? { kind: "loading" }
    : discoveryResource.value;
  const [externalNotice, setExternalNotice] = useState(externalLoginFailed);
  const [mfaRequired, setMFARequired] = useState(false);
  const institution = useHostedInstitution();

  return (
    <AccessPageShell skipLabel={message("webapp.login.skip_to_main")}>
      <section className={styles.page} aria-labelledby="login-heading">
        {mfaRequired ? (
          <AccessTaskIntro
            kind={message("webapp.login.mfa.kind")}
            heading={message("webapp.login.mfa.heading")}
            headingID="login-heading"
            body={message("webapp.login.mfa.lede")}
          />
        ) : (
          <header className={styles.intro}>
            <TaskHeading id="login-heading">
              {message("webapp.login.heading")}
            </TaskHeading>
            <p className={styles.lede}>
              {institution.displayName === undefined
                ? message("webapp.login.lede_unnamed")
                : message("webapp.login.lede", {
                    Institution: institution.displayName,
                  })}
            </p>
          </header>
        )}

        {externalNotice && !mfaRequired ? (
          <Notice role="note" tone="warning">
            <p>{message("webapp.login.external_failure")}</p>
          </Notice>
        ) : null}

        <div className={styles.discovery}>
          <DiscoveryContent
            authenticate={authenticateLocal}
            institutionName={institution.displayName}
            mfaRequired={mfaRequired}
            state={discoveryState}
            onMFARequiredChange={setMFARequired}
            onRetry={discoveryResource.retry}
            onAuthenticationAction={() => setExternalNotice(false)}
          />
        </div>
      </section>
    </AccessPageShell>
  );
}

interface DiscoveryContentProps {
  authenticate: AuthenticateLocal;
  institutionName?: string;
  mfaRequired: boolean;
  onAuthenticationAction(): void;
  onMFARequiredChange(required: boolean): void;
  onRetry(): void;
  state: LoginDiscoveryState;
}

function DiscoveryContent({
  authenticate,
  institutionName,
  mfaRequired,
  onAuthenticationAction,
  onMFARequiredChange,
  onRetry,
  state,
}: DiscoveryContentProps) {
  if (state.kind === "loading") {
    return (
      <Loading className={styles.loading} label={message("webapp.login.loading")} showLabel size="large" />
    );
  }
  if (state.kind === "setup") {
    return (
      <RouteState
        heading={message("webapp.login.setup.heading")}
        body={message("webapp.login.setup.body")}
      >
        <ButtonLink href="/setup">
          {message("webapp.login.setup.open")}
        </ButtonLink>
      </RouteState>
    );
  }
  if (state.kind === "origin_mismatch") {
    return (
      <RouteState
        heading={message("webapp.login.origin_mismatch.heading")}
        body={message("webapp.login.origin_mismatch.body")}
      >
        <Button onClick={() => window.location.reload()}>
          {message("webapp.login.reload")}
        </Button>
      </RouteState>
    );
  }
  if (state.kind === "failure" || state.kind === "unavailable") {
    const unavailable = state.kind === "unavailable";
    return (
      <RouteState
        heading={message(
          unavailable
            ? "webapp.login.unavailable.heading"
            : "webapp.login.discovery_failure.heading",
        )}
        body={message(
          unavailable
            ? "webapp.login.unavailable.body"
            : "webapp.login.discovery_failure.body",
        )}
      >
        <Button onClick={onRetry}>
          {message("webapp.login.retry")}
        </Button>
      </RouteState>
    );
  }
  return (
    <LoginMethods
      authenticate={authenticate}
      discovery={state.discovery}
      institutionName={institutionName}
      mfaRequired={mfaRequired}
      onAuthenticationAction={onAuthenticationAction}
      onMFARequiredChange={onMFARequiredChange}
    />
  );
}

function RouteState({
  body,
  children,
  heading,
}: {
  body: string;
  children: ReactNode;
  heading: string;
}) {
  return (
    <div className={styles.routeState}>
      <div className={styles.routeStateCopy}>
        <h2>{heading}</h2>
        <p>{body}</p>
      </div>
      <div className={styles.actions}>{children}</div>
    </div>
  );
}

function LoginMethods({
  authenticate,
  discovery,
  institutionName,
  mfaRequired,
  onAuthenticationAction,
  onMFARequiredChange,
}: {
  authenticate: AuthenticateLocal;
  discovery: PublicAccessDiscovery;
  institutionName?: string;
  mfaRequired: boolean;
  onAuthenticationAction(): void;
  onMFARequiredChange(required: boolean): void;
}) {
  const hasLocal = discovery.capabilities.local_login;
  const hasProviders = discovery.providers.length > 0 && !mfaRequired;

  return (
    <div className={styles.methods}>
      {hasLocal ? (
        <LocalLoginForm
          authenticate={authenticate}
          mfaRequired={mfaRequired}
          onAuthenticationAction={onAuthenticationAction}
          onMFARequiredChange={onMFARequiredChange}
        />
      ) : null}

      {hasLocal && hasProviders ? (
        <div className={styles.separator} aria-hidden="true">
          <span>{message("webapp.login.method_separator")}</span>
        </div>
      ) : null}

      {hasProviders ? (
        <fieldset className={styles.providerGroup}>
          <legend className={styles.visuallyHidden}>
            {message("webapp.login.provider.group")}
          </legend>
          {discovery.providers.map((provider) => (
            <Button
              key={provider.id}
              variant="secondary"
              onClick={() => {
                onAuthenticationAction();
                const parameters = new URLSearchParams({
                  client_type: "web",
                  return_to: "/authorization/complete",
                });
                navigateToProvider(
                  `/api/v1/auth/providers/${encodeURIComponent(provider.id)}/login?${parameters.toString()}`,
                );
              }}
            >
              {message("webapp.login.provider.continue", {
                Provider: provider.display_name,
              })}
            </Button>
          ))}
        </fieldset>
      ) : null}

      {mfaRequired ? null : (
        <section
          className={styles.newcomer}
          aria-labelledby="login-newcomer-heading"
        >
          <h2 id="login-newcomer-heading">
            {message("webapp.login.registration.prompt")}
          </h2>
          <p>
            {institutionName === undefined
              ? message("webapp.login.registration.invitation_unnamed")
              : message("webapp.login.registration.invitation", {
                  Institution: institutionName,
                })}
            {discovery.capabilities.public_registration ? (
              <>
                {" "}
                {richMessage("webapp.login.registration.open", {
                  Link: (
                    <a href="/register">
                      {message("webapp.login.registration.action")}
                    </a>
                  ),
                })}
              </>
            ) : null}
          </p>
        </section>
      )}
    </div>
  );
}

function LocalLoginForm({
  authenticate,
  mfaRequired,
  onAuthenticationAction,
  onMFARequiredChange,
}: {
  authenticate: AuthenticateLocal;
  mfaRequired: boolean;
  onAuthenticationAction(): void;
  onMFARequiredChange(required: boolean): void;
}) {
  const [loginID, setLoginID] = useState("");
  const [password, setPassword] = useState("");
  const [mfaCode, setMFACode] = useState<AuthenticationCodeValue>({ kind: "authenticator", code: "" });
  const [pending, setPending] = useState(false);
  const [fieldErrors, setFieldErrors] = useState<FieldErrors>({});
  const [formError, setFormError] = useState<string>();
  const [liveMessage, setLiveMessage] = useState("");
  const loginIDRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);
  const mfaCodeRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (mfaRequired) {
      mfaCodeRef.current?.focus();
    }
  }, [mfaRequired]);

  function clearFormFailure() {
    setFormError(undefined);
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) {
      return;
    }
    onAuthenticationAction();

    const nextErrors: FieldErrors = {};
    if (loginID.trim() === "") {
      nextErrors.login_id = message("webapp.login.form.error.username_required");
    }
    if (password === "") {
      nextErrors.password = message("webapp.login.form.error.password_required");
    }
    if (mfaRequired && !isCompleteAuthenticationCode(mfaCode)) {
      nextErrors.mfa_code = message("webapp.login.form.error.code_required");
    }
    setFieldErrors(nextErrors);
    setFormError(undefined);
    if (nextErrors.login_id !== undefined) {
      loginIDRef.current?.focus();
      return;
    }
    if (nextErrors.password !== undefined) {
      passwordRef.current?.focus();
      return;
    }
    if (nextErrors.mfa_code !== undefined) {
      mfaCodeRef.current?.focus();
      return;
    }

    setPending(true);
    setLiveMessage(message(mfaRequired ? "webapp.login.form.verifying" : "webapp.login.form.signing_in"));
    try {
      const result = await authenticate({
        loginID,
        password,
        ...(mfaRequired ? { mfaCode: mfaCode.code } : {}),
      });
      if (result.kind === "authenticated" || result.kind === "recovery_required" || result.kind === "enrollment_required") {
        setLoginID("");
        setPassword("");
        setMFACode({ kind: "authenticator", code: "" });
        // A restricted Session can only set up two-step verification.
        window.location.replace(result.kind === "authenticated" ? "/authorization/complete" : "/account/security");
        return;
      }

      if (result.kind === "mfa_required") {
        onMFARequiredChange(true);
        setMFACode({ kind: "authenticator", code: "" });
        setFieldErrors({});
        setFormError(undefined);
        setLiveMessage(message("webapp.login.mfa.lede"));
        return;
      }
      if (result.kind === "mfa_invalid") {
        const errorMessage = message("webapp.login.form.error.code_rejected");
        setFieldErrors({ mfa_code: errorMessage });
        setLiveMessage(errorMessage);
        requestAnimationFrame(() => mfaCodeRef.current?.focus());
        return;
      }
      if (result.kind === "invalid_credentials" && !mfaRequired) {
        // The credentials are rejected as a pair; the message sits under the
        // password, which is the field the person usually retypes.
        const errorMessage = message("webapp.login.form.error.credentials_rejected");
        setFieldErrors({ password: errorMessage });
        setLiveMessage(errorMessage);
        requestAnimationFrame(() => passwordRef.current?.focus());
        return;
      }

      let safeError: string;
      switch (result.kind) {
        case "invalid_credentials":
          safeError = message("webapp.login.form.error.credentials_rejected");
          break;
        case "mfa_unavailable":
          safeError = message("webapp.login.form.error.mfa_unavailable");
          break;
        case "session_limit":
          safeError = message("webapp.login.form.error.sessions_maximum");
          break;
        case "rate_limited":
          safeError = message("webapp.login.form.error.rate_limited");
          break;
        default:
          safeError = message("webapp.login.form.error.generic");
      }
      setFormError(safeError);
      setLiveMessage("");
    } catch {
      const safeError = message("webapp.login.form.error.generic");
      setFormError(safeError);
      setLiveMessage("");
    } finally {
      setPending(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} aria-busy={pending} noValidate>
      <div className={styles.visuallyHidden} aria-live="polite" aria-atomic="true">
        {liveMessage}
      </div>

      {mfaRequired ? (
        <AuthenticationCodeField
          ref={mfaCodeRef}
          id="mfa-code"
          name="mfa_code"
          value={mfaCode}
          errorMessage={fieldErrors.mfa_code}
          disabled={pending}
          onChange={(value) => {
            setMFACode(value);
            setFieldErrors((errors) => ({ ...errors, mfa_code: undefined }));
            clearFormFailure();
          }}
        />
      ) : (
        <>
          <InputField
            ref={loginIDRef}
            id="login-id"
            name="login_id"
            label={message("webapp.login.form.email_or_username")}
            type="text"
            autoCapitalize="none"
            autoComplete="username"
            spellCheck={false}
            value={loginID}
            errorMessage={fieldErrors.login_id}
            required
            onChange={(event) => {
              setLoginID(event.currentTarget.value);
              setFieldErrors((errors) => ({ ...errors, login_id: undefined }));
              clearFormFailure();
            }}
          />

          <PasswordField
            ref={passwordRef}
            id="password"
            name="password"
            label={message("webapp.login.form.password")}
            labelAccessory={
              <a href="/account/forgot-password">
                {message("webapp.login.form.forgot_password")}
              </a>
            }
            autoComplete="current-password"
            value={password}
            errorMessage={fieldErrors.password}
            hidePasswordLabel={message("webapp.form.password_hide")}
            showPasswordLabel={message("webapp.form.password_show")}
            toggleDisabled={pending}
            required
            onChange={(event) => {
              setPassword(event.currentTarget.value);
              setFieldErrors((errors) => ({ ...errors, password: undefined }));
              clearFormFailure();
            }}
          />
        </>
      )}

      <div className={styles.submission}>
        <div className={styles.actions}>
          <Button
            type="submit"
            isLoading={pending}
            loadingLabel={message(mfaRequired ? "webapp.login.form.verifying" : "webapp.login.form.signing_in")}
          >
            {message(mfaRequired ? "webapp.login.form.verify" : "webapp.login.form.sign_in")}
          </Button>
          {mfaRequired ? (
            <Button
              disabled={pending}
              variant="text"
              onClick={() => {
                onMFARequiredChange(false);
                setPassword("");
                setMFACode({ kind: "authenticator", code: "" });
                setFieldErrors({});
                setFormError(undefined);
                setLiveMessage("");
                requestAnimationFrame(() => passwordRef.current?.focus());
              }}
            >
              {message("webapp.login.form.mfa_back")}
            </Button>
          ) : null}
        </div>
        <FormFeedback message={formError} />
      </div>
    </form>
  );
}
