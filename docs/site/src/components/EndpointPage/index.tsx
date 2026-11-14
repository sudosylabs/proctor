import Link from '@docusaurus/Link';
import {useState, type ReactNode} from 'react';

import CodePanel from '@site/src/components/CodePanel';
import styles from './styles.module.css';

export type Field = {
  name: string;
  type: string;
  typeHref: string | null;
  required: boolean;
  description: string;
  rules: string[];
  pattern: string | null;
  values: string[] | null;
  valuesOpen?: boolean;
  children: Field[];
  variants: {label: string; description: string; fields: Field[]}[];
  map?: boolean;
  standard: {text: string; href: string; label: string} | null;
};

// Where a field list sits decides which marker matters: what a caller must
// send, or what a response may leave out.
export type FieldMode = 'request' | 'response';

function Chevron(): ReactNode {
  return (
    <svg aria-hidden="true" height="10" viewBox="0 0 12 12" width="10">
      <path d="M4.5 2.5L8 6l-3.5 3.5" fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.5" />
    </svg>
  );
}

// Long descriptions open on their first sentence; the rest is one click away.
function Description({text}: {text: string}): ReactNode {
  const [open, setOpen] = useState(false);
  const split = text.length > 180 ? text.match(/^(.{40,}?[.!?])\s+(?=[A-Z`"(])/) : null;
  if (!split) return <p className={styles.propDesc}>{text}</p>;
  return (
    <p className={styles.propDesc}>
      {open ? text : split[1]}{' '}
      <button aria-expanded={open} className={styles.moreText} onClick={() => setOpen(!open)} type="button">
        {open ? 'Less' : 'More'}
      </button>
    </p>
  );
}

function Disclosure({label, children}: {label: string; children: ReactNode}): ReactNode {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button aria-expanded={open} className={styles.disclose} onClick={() => setOpen(!open)} type="button">
        <Chevron />
        {open ? 'Hide' : 'Show'} {label}
      </button>
      {open ? <div className={styles.kids}>{children}</div> : null}
    </>
  );
}

function count(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

function FieldRow({field, mode}: {field: Field; mode: FieldMode}): ReactNode {
  return (
    <div className={styles.prop}>
      <div className={styles.propHead}>
        <span className={styles.propName}>{field.name}</span>
        {field.typeHref ? (
          <Link className={styles.propTypeLink} to={field.typeHref}>
            {field.type}
          </Link>
        ) : (
          <span className={styles.propType}>{field.type}</span>
        )}
        {mode === 'request' && field.required ? <span className={styles.required}>required</span> : null}
        {mode === 'response' && !field.required ? <span className={styles.optional}>optional</span> : null}
      </div>
      {field.standard ? (
        <p className={styles.propDesc}>
          {field.standard.text} <Link to={field.standard.href}>{field.standard.label}</Link>
        </p>
      ) : field.description ? (
        <Description text={field.description} />
      ) : null}
      {field.rules.length > 0 || field.values ? (
        <div className={styles.rules}>
          {field.rules.map((rule) => (
            <span key={rule}>{rule}</span>
          ))}
          {field.values ? (
            <span className={styles.values}>
              {field.valuesOpen ? 'Known values' : 'One of'}
              {field.values.map((value) => (
                <code key={value}>{value}</code>
              ))}
            </span>
          ) : null}
        </div>
      ) : null}
      {field.valuesOpen ? <p className={styles.valuesNote}>New values may be added later, so handle ones you don’t recognise.</p> : null}
      {field.pattern ? (
        <details className={styles.pattern}>
          <summary>
            <Chevron />
            Exact pattern
          </summary>
          <code>{field.pattern}</code>
        </details>
      ) : null}
      {field.variants.length > 0 ? (
        <Disclosure label={count(field.variants.length, 'shape', 'shapes')}>
          {field.variants.map((variant) => (
            <div className={styles.variant} key={variant.label}>
              <p className={styles.variantLabel}>
                <code>{variant.label}</code>
              </p>
              {variant.description ? <Description text={variant.description} /> : null}
              {variant.fields.map((child) => (
                <FieldRow field={child} key={child.name} mode={mode} />
              ))}
            </div>
          ))}
        </Disclosure>
      ) : null}
      {field.children.length > 0 ? (
        <Disclosure label={field.map ? 'what each value holds' : count(field.children.length, 'field', 'fields')}>
          {field.children.map((child) => (
            <FieldRow field={child} key={child.name} mode={mode} />
          ))}
        </Disclosure>
      ) : null}
    </div>
  );
}

export function Fields({fields, mode = 'request'}: {fields: Field[]; mode?: FieldMode}): ReactNode {
  return (
    <div className={styles.props}>
      {fields.map((field) => (
        <FieldRow field={field} key={field.name} mode={mode} />
      ))}
    </div>
  );
}


type Example = {status: number; label: string; ok: boolean; code: string; errorCode?: string; fromContract?: boolean};
type Sample = {id: string; label: string; language: string; code: string; highlight: string};
type Summary = {id: string; name: string; method: string; path: string; summary: string};

export type Endpoint = {
  id: string;
  name: string;
  method: string;
  path: string;
  tag: string;
  tagSlug: string;
  summary: string;
  description: string;
  auth: string;
  authLabel: string;
  idempotency: string;
  idempotencyLabel: string;
  pathParams: Field[];
  query: Field[];
  headers: Field[];
  cookies: Field[];
  body: {contentType: string; required: boolean; description: string; fields: Field[]; closed: boolean; list: boolean} | null;
  response: {status: number; label: string; contentType: string | null; description: string; fields: Field[]; list: boolean} | null;
  otherResponse: {status: number; label: string; description: string; headers: {name: string; description: string; type: string}[]} | null;
  errors: {status: number; label: string; codes: {code: string; meaning: string}[]}[];
  errorCount: number;
  examples: Example[];
  samples: Sample[];
  more: Summary[];
  kernel: {code: string; meaning: string}[];
};

export function PathText({path}: {path: string}): ReactNode {
  return (
    <>
      {path.split(/(\{[^}]+\})/).map((part, index) =>
        part.startsWith('{') ? (
          <span className={styles.var} key={index}>
            {part}
          </span>
        ) : (
          // Paths break only after a slash, never inside a segment.
          <span key={index}>
            {part.split('/').map((segment, position, all) => (
              <span key={position}>
                {segment}
                {position < all.length - 1 ? (
                  <>
                    /<wbr />
                  </>
                ) : null}
              </span>
            ))}
          </span>
        ),
      )}
    </>
  );
}

function Section({title, meta, children, id}: {title: string; meta?: ReactNode; children: ReactNode; id?: string}): ReactNode {
  return (
    <section className={styles.section} id={id}>
      <div className={styles.secHead}>
        <h2>{title}</h2>
        {meta ? <span className={styles.secMeta}>{meta}</span> : null}
      </div>
      {children}
    </section>
  );
}

function CopyPath({path}: {path: string}): ReactNode {
  const [copied, setCopied] = useState(false);
  return (
    <button
      aria-label={copied ? 'Path copied' : 'Copy path'}
      className={styles.copyPath}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(path);
          setCopied(true);
          window.setTimeout(() => setCopied(false), 1600);
        } catch {
          setCopied(false);
        }
      }}
      type="button">
      {copied ? (
        <svg aria-hidden="true" height="13" viewBox="0 0 14 14" width="13">
          <path d="M3 7.5l2.5 2.5L11 4.5" fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.5" />
        </svg>
      ) : (
        <svg aria-hidden="true" height="13" viewBox="0 0 14 14" width="13">
          <rect fill="none" height="7.5" rx="1.5" stroke="currentColor" strokeWidth="1.3" width="7.5" x="4.5" y="4.5" />
          <path d="M9.5 2.5H3.5a1 1 0 0 0-1 1v6" fill="none" stroke="currentColor" strokeLinecap="round" strokeWidth="1.3" />
        </svg>
      )}
    </button>
  );
}

export function MethodLabel({method}: {method: string}): ReactNode {
  return <span className={styles.method}>{method.toUpperCase()}</span>;
}

// One endpoint: what it does and needs on the left, a request and its
// responses on the right, kept in view while the contract scrolls.
export default function EndpointPage({data}: {data: Endpoint}): ReactNode {
  const [showAll, setShowAll] = useState<Record<number, boolean>>({});
  const visibleLimit = 8;
  const exampleTabs = data.examples.map((example) => ({
    id: String(example.status),
    label: String(example.status),
    language: 'json',
    code: example.code,
    tone: example.ok ? ('ok' as const) : ('bad' as const),
    note: example.ok ? (
      example.fromContract ? 'Example from the API contract.' : 'Built from the response schema.'
    ) : (
      <>
        One of the {data.errors.find((group) => group.status === example.status)?.codes.length ?? 1} {example.status} codes this endpoint can return.{' '}
        <Link to={`/problems/${example.errorCode}`}>What it means</Link>
      </>
    ),
  }));
  const sampleTabs = data.samples.map((sample) => ({
    ...sample,
    note: sample.highlight ? 'Body values come from the API contract’s example. Highlighted lines are the body.' : undefined,
  }));

  return (
    <div className={styles.page} data-endpoint-page data-plain-link>
      <article className={styles.main}>
        <h1 className={styles.title}>{data.name}</h1>
        <div className={styles.bar}>
          <MethodLabel method={data.method} />
          <code className={styles.path}>
            <PathText path={data.path} />
          </code>
          <CopyPath path={data.path} />
        </div>
        {data.summary ? <p className={styles.lead}>{data.summary}</p> : null}
        {data.description && data.description !== data.summary ? <p className={styles.description}>{data.description}</p> : null}

        <dl className={styles.reqs}>
          <div>
            <dt>Authentication</dt>
            <dd>
              {data.authLabel}{data.auth !== 'public' ? '.' : ''}
              {data.auth !== 'public' ? (
                <>
                  {' '}
                  <Link to="/api/guides/authentication">Which credentials</Link>
                </>
              ) : null}
            </dd>
          </div>
          <div>
            <dt>Idempotency-Key</dt>
            <dd>
              {data.idempotency === 'none' ? (
                'Not used'
              ) : (
                <Link to="/api/guides/idempotency">{data.idempotencyLabel}</Link>
              )}
            </dd>
          </div>
          <div>
            <dt>Errors</dt>
            <dd>{data.errorCount > 0 ? <a href="#errors">{data.errorCount} {data.errorCount === 1 ? 'code' : 'codes'}</a> : 'None specific'}</dd>
          </div>
        </dl>

        {data.pathParams.length > 0 ? (
          <Section title="Path parameters">
            <Fields fields={data.pathParams} />
          </Section>
        ) : null}
        {data.query.length > 0 ? (
          <Section title="Query parameters">
            <Fields fields={data.query} />
          </Section>
        ) : null}
        {data.headers.length > 0 ? (
          <Section title="Headers">
            <Fields fields={data.headers} />
          </Section>
        ) : null}
        {data.cookies.length > 0 ? (
          <Section title="Cookies">
            <Fields fields={data.cookies} />
          </Section>
        ) : null}

        {data.body ? (
          <Section
            meta={
              <>
                <code>{data.body.contentType}</code>
                {data.body.closed ? '. Other fields are rejected.' : null}
              </>
            }
            title="Request body">
            {data.body.description ? <p className={styles.sectionLead}>{data.body.description}</p> : null}
            {data.body.fields.length > 0 ? (
              <Fields fields={data.body.fields} />
            ) : (
              <p className={styles.sectionLead}>
                Send the file itself as the body, with <code>Content-Type: {data.body.contentType}</code>.
              </p>
            )}
          </Section>
        ) : null}

        {data.response ? (
          <Section
            meta={
              <>
                <span className={styles.ok}>{data.response.status}</span> {data.response.label}
                {data.response.contentType ? (
                  <>
                    {' · '}
                    <code>{data.response.contentType}</code>
                  </>
                ) : null}
              </>
            }
            title="Response">
            {data.response.description ? <p className={styles.sectionLead}>{data.response.description}</p> : null}
            {data.response.fields.length > 0 ? <Fields fields={data.response.fields} mode="response" /> : null}
          </Section>
        ) : null}

        {data.otherResponse ? (
          <Section
            meta={
              <>
                <span className={styles.ok}>{data.otherResponse.status}</span> {data.otherResponse.label}
              </>
            }
            title="Response">
            <p className={styles.sectionLead}>
              {data.otherResponse.status === 101
                ? 'The connection switches to the WebSocket protocol. There is no response body; messages follow on the socket.'
                : 'There is no response body. The browser follows the Location header to the next step of sign-in.'}{' '}
              {data.otherResponse.status === 101 ? <Link to="/api/reference/realtime-protocol">How the protocol works</Link> : null}
            </p>
            {data.otherResponse.headers.length > 0 ? (
              <Fields
                fields={data.otherResponse.headers.map((header) => ({
                  name: header.name,
                  type: header.type,
                  typeHref: null,
                  required: true,
                  description: header.description,
                  rules: [],
                  pattern: null,
                  values: null,
                  children: [],
                  variants: [],
                  standard: null,
                }))}
                mode="response"
              />
            ) : null}
          </Section>
        ) : null}

        {data.errors.length > 0 ? (
          <Section id="errors" meta="Each code links to what causes it and how to fix it" title="Errors">
            {data.errors.map((group) => {
              const expanded = showAll[group.status] || group.codes.length <= visibleLimit + 1;
              const codes = expanded ? group.codes : group.codes.slice(0, visibleLimit);
              return (
                <div className={styles.errGroup} key={group.status}>
                  <div className={styles.errHead}>
                    <span className={styles.errStatus}>{group.status}</span>
                    <span>{group.label}</span>
                  </div>
                  {codes.map((entry) => (
                    <Link className={styles.errRow} key={entry.code} to={`/problems/${entry.code}`}>
                      <span className={styles.errCode}>{entry.code}</span>
                      <span className={styles.errMeaning}>{entry.meaning}</span>
                    </Link>
                  ))}
                  {!expanded ? (
                    <button className={styles.showMore} onClick={() => setShowAll({...showAll, [group.status]: true})} type="button">
                      Show {group.codes.length - visibleLimit} more {group.status} codes
                    </button>
                  ) : null}
                </div>
              );
            })}
            <p className={styles.kernel}>
              Any request can also return{' '}
              {data.kernel.map((entry, index) => (
                <span key={entry.code}>
                  <Link to={`/problems/${entry.code}`}>
                    <code>{entry.code}</code>
                  </Link>
                  {index < data.kernel.length - 2 ? ', ' : index === data.kernel.length - 2 ? ' and ' : '.'}
                </span>
              ))}
            </p>
          </Section>
        ) : null}

        {data.more.length > 0 ? (
          <Section title={`More in ${data.tag}`}>
            <div className={styles.more}>
              {data.more.map((other) => (
                <Link className={styles.moreRow} key={other.id} to={`/api/reference/${other.id}`}>
                  <MethodLabel method={other.method} />
                  <span className={styles.moreName}>{other.name}</span>
                  <code className={styles.morePath}>
                    <PathText path={other.path} />
                  </code>
                </Link>
              ))}
            </div>
          </Section>
        ) : null}
      </article>

      <aside aria-label="Example request and response" className={styles.aside}>
        <CodePanel label="Request sample" tabs={sampleTabs} />
        {exampleTabs.length > 0 ? <CodePanel label="Response examples" tabs={exampleTabs} /> : null}
      </aside>
    </div>
  );
}
