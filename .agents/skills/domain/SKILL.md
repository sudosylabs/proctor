---
name: domain
description: Work that touches what Proctor's concepts mean. Use for naming, academic structure, exams, sittings, attempts, integrity and review, identity and onboarding, files and workspaces, user settings and transactional mail.
---

# Speaking Proctor's domain

You are a product-minded engineer who knows how exams are run in a school or
university. Proctor's words are precise on purpose. Collapsing two of them is
how bugs, privacy leaks and unfair accusations start.

## How to work

- **Use the canonical terms.** They are in `docs/glossary.md`, which also
  generates the public glossary. When you change a definition, run
  `npm run generate:glossary` and `check:glossary` in `docs/site`.
- **Before adding a concept, check whether an existing term already carries
  it.** Add a new term only when none can carry it without ambiguity.
- **Don't infer behavior from similar products.** If the domain rule isn't
  written in the code, the glossary or the references below, ask the owner.
  Proctor deliberately leaves out a lot: grading, appeals, accommodations and
  a proctor role are all undecided.

## The confusions that cause real bugs

- **The core exam terms.** A **Sitting** delivers one Exam Revision to one
  Class; it is not a "session" or an "exam version". An **Attempt** is one
  student's work. A **Participation** is one fenced period of presence. A
  **Connection** is one transport link. A Participation Lease is not a ping.
- **Suspicion is not guilt.** An **Integrity Flag** or a Focus Loss is
  suspicion. A **Review** records Dismissed, Confirmed or Inconclusive, never
  a grade. Candidate-facing copy never accuses.
- **Roles.** An **Exam Manager** is never a "proctor" or a "grader".
- **Similar names that differ.** A **Job Attempt** is not an Exam Attempt.
- **What a User is not.** A **User** holds no credentials, Affiliations or
  Roles. An **Affiliation** is not a Role. A **Session** carries no role
  snapshot. A Personal Access Token is not a Session. An **Invitation** is not
  a User Token.
- **Academic structure terms.** A **Class** is not a "group" or "cohort". An
  **Academic Unit** is generic, not a department or faculty. A **Programme
  Level** is not a "year". Diagrams never shorten terms.
- **One installation is one institution.** There are no tenants.

## Rules that hold across the product

- **Grading is out of scope.** Scores, rubrics and pass/fail are outside the
  product.
- **Membership grants nothing.** Being in a Class or an Academic Unit gives no
  permission; Role Bindings do.
- **Identity.** One person is one User. Email and username are never external
  identity keys, and there is no User merge.
- **Submissions** are terminal and single: no reopen, no resubmit.
- **Candidate content** stays inside the product: no download, export, print
  or external open.
- **Mail is mandatory communication.** It is sent after the business commit
  through durable delivery, and never carries answers, scores, evidence or
  tokens.

## References

Read the one for your area before changing it:
- [Exams, sittings, attempts and integrity](references/exams.md)
- [Identity, access and onboarding](references/identity.md)
- [Academic structure, files, user settings and mail](references/structure-files-mail.md)

## Good examples to read first

- `server/model/doc.go`
- `server/app/exam/doc.go`
- `server/cluster/GUARANTEES.md`
- `docs/public/developers/server/user-settings-slice.mdx`
