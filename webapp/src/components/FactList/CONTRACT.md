# Fact list contract

`FactList` presents a short set of labelled facts the person should check
before or after an action: an Invitation's role and expiry, a Desktop
authorization request's account and device, an email change being undone,
what setup created, or account-security status. It is a native description
list separated by hairline rules, not a card, table, or key-value grid with
backgrounds.

```tsx
<FactList aria-label="…">
  <Fact label="Invited as">Student</Fact>
  <Fact label="Setup key" mono>R7KQ-2MVD</Fact>
</FactList>
```

| Component | Props | Meaning |
| --- | --- | --- |
| `FactList` | native `dl` attributes, including `className` and `aria-*` | The list; `className` is for placement only |
| `Fact` | `label: ReactNode`, `children: ReactNode`, `mono?: boolean` | One `dt`/`dd` pair; `mono` sets the value in the data face for codes and identifiers |

Each fact is one `div` grouping a `dt` and `dd`, which is valid description
list content and keeps pairs together for assistive technology. Labels are
muted small-body text; values use the body size and default foreground and
wrap anywhere, so long names, email addresses, and identifiers never force
horizontal scrolling. Values render as escaped text or page-owned inline
elements such as a state word; the component adds no icons or colour.

When the list's own inline size reaches `30rem` (a container query), each fact
becomes two columns with the label taking about 38%. Narrower, and wherever
container queries are unsupported, the label stacks above the value in
document order.

The page owns which facts appear, their copy and formatting (`Intl` dates,
masking), and any heading or accessible name. The component owns only the
rules, rhythm, responsive arrangement, mono face, and forced-colors borders.
`FactList.test.tsx` covers structure, the mono value, and escaping.
