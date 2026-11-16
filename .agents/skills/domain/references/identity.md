# Identity, access and onboarding

- **External identity:** it is the immutable `(provider ID, opaque subject)`.
  Auto-provisioning never links an existing account by email or username,
  because that is how accounts are taken over. Provider claims never create
  Affiliations, memberships or Role Bindings.
- **Bootstrap:** it is one explicit, serialized operation, never a side effect
  of the first User. It mints no Session, and the administrator's email starts
  unverified. Server invariants, not UI advice, keep the last system-admin
  sign-in path.
- **Host recovery:** it is offline only. Every node is stopped, and it refuses
  while any serving lease is alive.
- **Access Policy:** it is composable, not a `local`/`external`/`hybrid` enum.
  - Provider modes are `disabled`, `linked_only`, `invitation_required` and
    `auto_provision`.
  - UI labels never drive policy.
  - Removing a provider from config keeps its policy and links.
- **Role-holder MFA:** `role_holder_mfa_required` pauses rather than locks out
  when MFA is disabled. A role holder means a current Role Binding;
  Affiliation, even "staff member", is unrelated.
- **Invitation mail:** `invitation_required` can't be enabled without healthy
  durable mail, and no API ever returns a raw Invitation link.
- **Invitation packages:** they are closed: `student_class`,
  `teacher_academic_unit`, `academic_unit_role` and `institution_role`. There
  is no empty Invitation.
  - The secret travels only in a URL fragment, never in CAS or OIDC `state`.
  - The effective start is the later of the intended start and acceptance.
- **Permissions imply nothing more.** Invitation and batch permissions don't
  imply their effects, and generic `job.*` permissions never grant
  onboarding.
- **The Session lock:** one unresolved Attempt holds the account's Session
  lock. Session issuance, Desktop exchange, Attempt activation and revocation
  all take it.
- **Desktop:**
  - loopback callbacks are the IP literals `127.0.0.1` or `[::1]`, never
    `localhost`, and at most 1024 bytes;
  - Desktop tokens are DPoP-bound to a registered ES256 key.
- **Sign-in throttling:** login throttling never refuses a correct password
  because of account-wide evidence. It only adds a bounded delay, so that
  attempts by others cannot lock an account out. Changing this is a security
  policy change.
- **CSV import:**
  - at most 10 MiB and 50,000 rows;
  - the original bytes are deleted after the preview;
  - rows are independent transactions;
  - reports escape spreadsheet formulas;
  - previews last 7 days.
- **Unit-scoped visibility:** unit-scoped `user.view` reaches only Users
  related within the subtree, and never reveals that a User is disabled. Unit
  administrators end relationships; they don't disable Users.
