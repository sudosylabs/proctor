import { type FormEvent, useRef, useState } from "react";

import type {
  SetupCreated,
  SetupSubmission,
  SetupSubmissionResult,
} from "../../features/setup/SetupApi";
import { message } from "../../i18n/messages";
import { richMessage } from "../../i18n/richMessage";
import { AccessTaskIntro } from "../AccessTaskIntro/AccessTaskIntro";
import { Button } from "../Button/Button";
import { FormFeedback } from "../FormFeedback/FormFeedback";
import { InputField } from "../InputField/InputField";
import { PasswordField } from "../InputField/PasswordField";
import styles from "./Setup.module.css";
import { passwordRejectionMessage } from "../../auth/passwordRejection";

type SetupField =
  | "institution_display_name"
  | "institution_name"
  | "administrator_email"
  | "administrator_username"
  | "password"
  | "bootstrap_secret";

type FieldErrors = Partial<Record<SetupField, string>>;

/** Validation and focus follow the visible field order. */
const fieldOrder: readonly SetupField[] = [
  "institution_display_name",
  "institution_name",
  "administrator_email",
  "administrator_username",
  "password",
  "bootstrap_secret",
];

export interface SetupFormProps {
  /** Receives what was created, or nothing when setup already existed. */
  onComplete(created?: SetupCreated): void;
  submitSetup(submission: SetupSubmission): Promise<SetupSubmissionResult>;
}

/** The setup kind line, page heading, and lede. */
export function SetupIntro() {
  return (
    <AccessTaskIntro
      kind={message("webapp.setup.kind")}
      heading={message("webapp.setup.heading")}
      headingID="setup-heading"
      body={message("webapp.setup.lede")}
    />
  );
}

export function SetupForm({ onComplete, submitSetup }: SetupFormProps) {
  const [bootstrapSecret, setBootstrapSecret] = useState("");
  const [institutionName, setInstitutionName] = useState("");
  const [institutionDisplayName, setInstitutionDisplayName] = useState("");
  const [institutionDescription, setInstitutionDescription] = useState("");
  const [administratorEmail, setAdministratorEmail] = useState("");
  const [administratorUsername, setAdministratorUsername] = useState("");
  const [administratorDisplayName, setAdministratorDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [pending, setPending] = useState(false);
  const [fieldErrors, setFieldErrors] = useState<FieldErrors>({});
  const [formError, setFormError] = useState<string>();
  const [liveMessage, setLiveMessage] = useState("");

  const bootstrapSecretRef = useRef<HTMLInputElement>(null);
  const institutionNameRef = useRef<HTMLInputElement>(null);
  const institutionDisplayNameRef = useRef<HTMLInputElement>(null);
  const administratorEmailRef = useRef<HTMLInputElement>(null);
  const administratorUsernameRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);

  const fieldRefs: Record<SetupField, React.RefObject<HTMLInputElement | null>> = {
    bootstrap_secret: bootstrapSecretRef,
    institution_name: institutionNameRef,
    institution_display_name: institutionDisplayNameRef,
    administrator_email: administratorEmailRef,
    administrator_username: administratorUsernameRef,
    password: passwordRef,
  };

  function clearFieldError(field: SetupField) {
    setFieldErrors((errors) => ({ ...errors, [field]: undefined }));
    setFormError(undefined);
  }

  function focusField(field: SetupField) {
    requestAnimationFrame(() => fieldRefs[field].current?.focus());
  }

  function showFieldFailure(field: SetupField, error: string) {
    setFieldErrors((errors) => ({ ...errors, [field]: error }));
    setFormError(undefined);
    setLiveMessage(error);
    focusField(field);
  }

  function showFormFailure(error: string) {
    setFormError(error);
    setLiveMessage("");
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) {
      return;
    }

    const nextErrors: FieldErrors = {};
    if (institutionDisplayName.trim() === "") {
      nextErrors.institution_display_name = message(
        "webapp.setup.form.error.institution_display_name_required",
      );
    }
    if (institutionName.trim() === "") {
      nextErrors.institution_name = message(
        "webapp.setup.form.error.institution_name_required",
      );
    }
    if (administratorEmail.trim() === "") {
      nextErrors.administrator_email = message(
        "webapp.setup.form.error.email_required",
      );
    } else if (administratorEmailRef.current?.validity.typeMismatch) {
      nextErrors.administrator_email = message(
        "webapp.setup.form.error.email_invalid",
      );
    }
    if (administratorUsername.trim() === "") {
      nextErrors.administrator_username = message(
        "webapp.setup.form.error.username_required",
      );
    }
    if (password === "") {
      nextErrors.password = message(
        "webapp.setup.form.error.password_required",
      );
    }
    if (bootstrapSecret === "") {
      nextErrors.bootstrap_secret = message(
        "webapp.setup.form.error.bootstrap_secret_required",
      );
    }
    setFieldErrors(nextErrors);
    setFormError(undefined);

    const firstInvalid = fieldOrder.find(
      (field) => nextErrors[field] !== undefined,
    );
    if (firstInvalid !== undefined) {
      focusField(firstInvalid);
      return;
    }

    setPending(true);
    setLiveMessage(message("webapp.setup.form.submitting"));
    try {
      const result = await submitSetup({
        bootstrapSecret,
        institutionName,
        institutionDisplayName,
        institutionDescription,
        administratorEmail,
        administratorUsername,
        administratorDisplayName,
        password,
      });

      if (result.kind === "complete" || result.kind === "already_initialized") {
        setBootstrapSecret("");
        setPassword("");
        onComplete(
          result.kind === "complete"
            ? {
                institutionDisplayName: institutionDisplayName.trim(),
                administratorName:
                  administratorDisplayName.trim() || administratorUsername.trim(),
                administratorEmail: administratorEmail.trim(),
              }
            : undefined,
        );
        return;
      }

      switch (result.kind) {
        case "bootstrap_denied":
          showFieldFailure(
            "bootstrap_secret",
            message("webapp.setup.form.error.bootstrap_denied"),
          );
          return;
        case "password_rejected":
          showFieldFailure(
            "password",
            message(
              passwordRejectionMessage(
                result.reason,
                "webapp.setup.form.error.password_invalid",
              ),
            ),
          );
          return;
        case "rate_limited":
          showFormFailure(message("webapp.setup.form.error.rate_limited"));
          return;
        default:
          showFormFailure(message("webapp.setup.form.error.generic"));
      }
    } catch {
      showFormFailure(message("webapp.setup.form.error.generic"));
    } finally {
      setPending(false);
    }
  }

  return (
    <section className={styles.page} aria-labelledby="setup-heading">
      <SetupIntro />

      <form className={styles.form} onSubmit={submit} aria-busy={pending} noValidate>
        <div className={styles.visuallyHidden} aria-live="polite" aria-atomic="true">
          {liveMessage}
        </div>

        <InputField
          ref={institutionDisplayNameRef}
          id="institution-display-name"
          name="institution_display_name"
          label={message("webapp.setup.form.institution.display_name")}
          type="text"
          autoComplete="organization"
          value={institutionDisplayName}
          errorMessage={fieldErrors.institution_display_name}
          required
          onChange={(event) => {
            setInstitutionDisplayName(event.currentTarget.value);
            clearFieldError("institution_display_name");
          }}
        />
        <InputField
          ref={institutionNameRef}
          id="institution-name"
          name="institution_name"
          label={message("webapp.setup.form.institution.name")}
          description={message("webapp.setup.form.institution.name_help")}
          type="text"
          autoCapitalize="none"
          autoComplete="off"
          spellCheck={false}
          value={institutionName}
          errorMessage={fieldErrors.institution_name}
          required
          onChange={(event) => {
            setInstitutionName(event.currentTarget.value);
            clearFieldError("institution_name");
          }}
        />
        <InputField
          id="institution-description"
          name="institution_description"
          label={message("webapp.setup.form.institution.description")}
          type="text"
          autoComplete="off"
          value={institutionDescription}
          onChange={(event) => setInstitutionDescription(event.currentTarget.value)}
        />

        <InputField
          id="administrator-display-name"
          name="administrator_display_name"
          label={message("webapp.setup.form.administrator.display_name")}
          type="text"
          autoComplete="name"
          value={administratorDisplayName}
          onChange={(event) => setAdministratorDisplayName(event.currentTarget.value)}
        />
        <InputField
          ref={administratorEmailRef}
          id="administrator-email"
          name="administrator_email"
          label={message("webapp.setup.form.administrator.email")}
          type="email"
          inputMode="email"
          autoCapitalize="none"
          autoComplete="email"
          spellCheck={false}
          value={administratorEmail}
          errorMessage={fieldErrors.administrator_email}
          required
          onChange={(event) => {
            setAdministratorEmail(event.currentTarget.value);
            clearFieldError("administrator_email");
          }}
        />
        <InputField
          ref={administratorUsernameRef}
          id="administrator-username"
          name="administrator_username"
          label={message("webapp.setup.form.administrator.username")}
          description={message("webapp.setup.form.administrator.username_help")}
          type="text"
          autoCapitalize="none"
          autoComplete="username"
          spellCheck={false}
          value={administratorUsername}
          errorMessage={fieldErrors.administrator_username}
          required
          onChange={(event) => {
            setAdministratorUsername(event.currentTarget.value);
            clearFieldError("administrator_username");
          }}
        />
        <PasswordField
          ref={passwordRef}
          id="administrator-password"
          name="password"
          label={message("webapp.setup.form.administrator.password")}
          description={message("webapp.setup.form.password_help")}
          autoComplete="new-password"
          value={password}
          errorMessage={fieldErrors.password}
          hidePasswordLabel={message("webapp.form.password_hide")}
          showPasswordLabel={message("webapp.form.password_show")}
          toggleDisabled={pending}
          required
          onChange={(event) => {
            setPassword(event.currentTarget.value);
            clearFieldError("password");
          }}
        />

        <PasswordField
          ref={bootstrapSecretRef}
          id="bootstrap-secret"
          name="bootstrap_secret"
          inputClassName={styles.secret}
          label={message("webapp.setup.form.operator.secret")}
          description={richMessage("webapp.setup.form.operator.secret_help", {
            Setting: <code>PROCTOR_AUTHENTICATION_BOOTSTRAP_SECRET</code>,
          })}
          autoComplete="off"
          spellCheck={false}
          value={bootstrapSecret}
          errorMessage={fieldErrors.bootstrap_secret}
          hidePasswordLabel={message("webapp.setup.form.secret_hide")}
          showPasswordLabel={message("webapp.setup.form.secret_show")}
          toggleDisabled={pending}
          required
          onChange={(event) => {
            setBootstrapSecret(event.currentTarget.value);
            clearFieldError("bootstrap_secret");
          }}
        />

        <div className={styles.submission}>
          <div className={styles.actions}>
            <Button
              type="submit"
              isLoading={pending}
              loadingLabel={message("webapp.setup.form.submitting")}
            >
              {message("webapp.setup.form.submit")}
            </Button>
          </div>
          <FormFeedback message={formError} />
        </div>
      </form>
    </section>
  );
}
