# Focused task-state contract

`TaskState` is the terminal, loading, unavailable, and context-replacement
skeleton proven by password recovery, email verification, email-change undo,
Invitation acceptance, provider connection, and Desktop authorization. It owns an optional
status line, the display-scale `h1`, supporting body, and responsive action
rhythm. Feature modules retain state, copy, evidence, and available actions.

`status` (`{ label, tone }`) appears only when the state is a real outcome,
and its label is that outcome in a few words (“Password changed”, “Link
expired”), never the feature's name.
`TaskStatusLine` renders it as the tone's icon (`stateSuccess`,
`stateWarning`, or `stateDanger`) beside the label in the matching state
colour at semibold weight; colour, shape, and words always travel together.
There is no neutral kind line: a state with nothing to report shows only its
heading. `AccessTaskIntro` uses the same status line.

During initial or replacement loading, `busy` keeps the heading visible and
shows the shared Loading indicator with its supporting message, aligned to the
column's start, in a reserved `10rem` region so the page does not jump when
the result arrives. The heading does not receive focus until a completed
user-triggered replacement requires it. Loading owns no page height.
The feature's existing TaskStateAnnouncement remains the one live region.

`TaskStateAnnouncement` supplies the persistent polite live region that a
feature keeps mounted while its active task changes. It announces bounded
localized state copy, never credentials, identifiers, or arbitrary server
prose. `TaskStateActions` keeps document order: a primary or danger action
spans the column, and secondary and text actions follow it, stacking below
`30rem`.

`TaskHeading` supports opt-in programmatic focus only after a user-triggered
replacement removes the invoking control and orientation would otherwise be
lost. Initial loading, background completion, and ordinary status updates do
not focus headings. Validation continues to focus the first invalid control.

A feature whose task moves through several states may key `TaskState` by
state so that a completed action remounts the heading and `focusHeading`
applies to the new outcome. This contract excludes the Session-confirmation
state rail. It also does not replace `Notice` stable evidence or
`FormFeedback` action-adjacent failures.
