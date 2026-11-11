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

Invoke
[`$server-boundaries`](../../.agents/skills/server-boundaries/SKILL.md)
before changing import policy and invoke
[`$documentation-design`](../../.agents/skills/documentation-design/SKILL.md)
before changing documentation or skill validation.

- Tests remain hermetic and network-free.
- A rule checked over the tree has a planted-violation test beside it that
  proves the check rejects what it claims to; an allowlist entry carries its
  reason and fails once it is stale.
- Production package boundaries are declared as ordered rules in
  `import_policy_test.go`. Every production package must match a rule and every
  forbidden import fails the gate; do not add waiver or debt mechanisms.
- Documentation validation inspects repository candidates when Git metadata is
  available and skips only the Git-specific check for an independently tested
  server module without a repository checkout.
- Report every violating file/link in one run so maintainers can repair the
  complete documentation graph.

Run `make -C server architecture` after changes here.

## Retired architecture tests

These tests enforced style or shape and were removed. Do not bring one back
as a test; extend the linter configuration or leave the rule to review.

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
| `TestRecordedCallLogsAreAssertedWhole` | review | Test style keyed on variable names (`events`, `order`, `calls`). |
| `TestNamedCaseTablesRunAsSubtests` | review | Test style; no linter expresses it. |
| `TestAdaptersDoNotWrapAdapters` | review | Shape; it recognized adapters by the form of their code. |
| `TestAuthorizationHelpersShareOneShape` | review | Shape; the authorization boundary tests guard where decisions happen. |
| `TestServicesTakeOneDependencies` | review | Shape of constructors. |
| `TestStoreMutationsTakeOneInput` | review | Shape of Store method signatures. |
| `TestStoreRevisionFencesNameTheirResource` | review | Shape of field names in Store inputs. |
| `TestApplicationDeclaresNoForwardingNames` | review | Shape; the import policy already decides who may name what. |

Each `...RejectsPlantedViolations` test left with its rule.
