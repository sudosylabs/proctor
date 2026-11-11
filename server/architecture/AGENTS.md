# Architecture-test agent guide

This directory holds the tests that protect boundaries and security
properties: which package may import which, where authorization and audit
happen, what the Store and the wire carry, what is persisted and how, and
that the documentation and license notices stay sound. A test belongs here
when breaking its rule would harm a user, an operator or an auditor.

Two kinds of rule do not belong here:

- Style (receiver names, initialisms, import aliases, spelling, how testify
  is called) goes to a linter in the root `.golangci.yml`. The one spelling
  test here stays because it reads SQL, templates and persisted values, which
  no Go linter does.
- Code shape (how a constructor takes its collaborators, how an input struct
  or a helper is laid out) is a convention in the
  [server guide](../AGENTS.md) that review holds, not a test.

Two tests look like shape rules but stay because each shape is a security
property:

- `TestAuthorizationHelpersShareOneShape`: an `Authorize…` helper takes the
  Invocation and reports its decision as the error, so none decides for
  nobody or fails open on a `(bool, error)` result checked only for its error.
- `TestRecordedCallLogsAreAssertedWhole`: a fake's recorded call log is
  compared whole, so a test notices an extra write, publication or audit
  completion after a commit.

Invoke
[`$server-boundaries`](../../.agents/skills/server-boundaries/SKILL.md)
before changing import policy and invoke
[`$documentation-design`](../../.agents/skills/documentation-design/SKILL.md)
before changing documentation or skill validation.

- Tests remain hermetic and network-free.
- A rule checked over the tree has a planted-violation test beside it that
  proves the check rejects what it claims to. An allowlist entry carries its
  reason; an import rule's allowance fails once no package the rule owns
  imports it (`TestDependencyAllowancesAreUsed`), but other allowlists are not
  checked for staleness, so remove an entry when its last use goes.
- Production package boundaries are declared once, as ordered rules in
  `dependencyRules` (`import_policy_test.go`). Every production package must
  match a rule and every forbidden import fails the gate; do not add waiver or
  debt mechanisms. Do not restate a rule in a per-package test or in prose: the
  planted cases in `imports_test.go` exercise each matcher shape and security
  edge, and the rule table in the
  [dependency reference](../../.agents/skills/server-boundaries/references/dependencies.md)
  is generated (`TestDependencyReferenceMatchesRules`).
- Documentation validation inspects repository candidates when Git metadata is
  available and skips only the Git-specific check for an independently tested
  server module without a repository checkout.
- Report every violating file/link in one run so maintainers can repair the
  complete documentation graph.

Run `make -C server architecture` after changes here.

## Retired architecture tests

These tests enforced style or shape, restated another rule, or only kept
retired names retired, and were removed. Do not bring one back as a test;
extend the linter configuration, the rule that covers it, or leave it to
review.

| Retired test | Replaced by | Reason |
| --- | --- | --- |
| `TestReceiverNamesAreShortAndConsistent` | revive `receiver-naming` (three letters at most), staticcheck `ST1016` | Style a standard linter already checks. |
| `TestDeclaredIdentifiersSpellIDAsAnInitialism` | staticcheck `ST1003`, revive `var-naming` | Style; the linters hold every initialism, not only `ID`. |
| `TestInternalImportsUseTheirPackageName` | `importas` | Style; each colliding package now has one listed alias. |
| `TestFilesAreNamedForTheSingularConcept` | review | Style; no linter names files and a wrong name harms nobody. |
| `TestNamesCarryNoVersionSuffix` | review | Naming; versioned names and operation spelling only look different before the first release. |
| `TestTestsAssertThroughTestify` | review | Test style; `packages/mail` uses plain `testing` on purpose and the shared linter paths cannot tell the modules apart. |
| `TestAssertionsAreNotHiddenInConditionals` | review | Test style; no `testifylint` checker expresses it. |
| `TestTestHelpersAreNotAssertionAliases` | review | Test style; no linter expresses it. |
| `TestNamedCaseTablesRunAsSubtests` | review | Test style; no linter expresses it. |
| `TestAdaptersDoNotWrapAdapters` | review | Shape; it recognized adapters by the form of their code. |
| `TestServicesTakeOneDependencies` | review | Shape of constructors. |
| `TestStoreMutationsTakeOneInput` | review | Shape of Store method signatures. |
| `TestStoreRevisionFencesNameTheirResource` | review | Shape of field names in Store inputs. |
| `TestApplicationDeclaresNoForwardingNames` | review | Shape; the import policy already decides who may name what. |
| `TestAreaPackageBoundaries` | `dependencyRules`, `TestApplicationPackagesPointInward` | Restated each area's import rule. |
| `TestExamRealtimePublisherAllowlist` | `dependencyRules` (the Exam, realtime, safe Markdown and Exam Manager access rules) | Restated the rules. |
| `TestInvocationLeafBoundary` | `dependencyRules` ("invocation application") | Restated the rule. |
| `TestExamManagerAccessBoundary` | `dependencyRules` (the Exam rules and "exam manager access") | Restated the rules. |
| `TestExamIdempotencyConsumerAllowlist` | `dependencyRules` (the Exam rules); an unlisted Exam child fails closed | Restated the rules. |
| `TestUserSettingsApplicationBoundary` | `dependencyRules` ("user application") | One file's import allowlist; the package rule already forbids its targets. |
| `TestApplicationFacadeDoesNotRetainPersistenceLocator` | `TestApplicationSurfaceIsNamedServices` | App holds only service pointers, never a Store contract, and declares no Store accessor. |
| `TestApplicationDoesNotExportFocusedServiceImplementations` | review | Kept one retired type name retired. |
| `TestRealtimeContractsHaveOneProductionOwner` | `dependencyRules`, `TestRealtimePeerContractsAreDeclaredOnlyByChild`, review | Kept retired parent names retired; the mechanics live in `app/realtime`, and a second name for a type is a review rule. |

Each `...RejectsPlantedViolations` test left with its rule.
