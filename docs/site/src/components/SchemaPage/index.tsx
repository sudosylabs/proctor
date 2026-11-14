import Link from '@docusaurus/Link';
import type {ReactNode} from 'react';

import CodePanel from '@site/src/components/CodePanel';
import {Fields, MethodLabel, PathText, type Field} from '@site/src/components/EndpointPage';
import endpoint from '@site/src/components/EndpointPage/styles.module.css';
import styles from './styles.module.css';

type User =
  | {kind: 'endpoint'; key: string; id: string; name: string; method: string; path: string}
  | {kind: 'type'; key: string; name: string; slug: string};

export type SharedType = {
  name: string;
  slug: string;
  description: string;
  fields: Field[];
  closed: boolean;
  example: string;
  usedBy: User[];
};

// One object type that several endpoints share: its fields once, and where it
// appears, so endpoint pages can link here instead of repeating it.
export default function SchemaPage({data}: {data: SharedType}): ReactNode {
  const endpoints = data.usedBy.filter((user) => user.kind === 'endpoint');
  const types = data.usedBy.filter((user) => user.kind === 'type');
  const sent = /Request$/.test(data.name);
  return (
    <div className={endpoint.page} data-endpoint-page data-plain-link>
      <article className={endpoint.main}>
        <h1 className={endpoint.title}>{data.name}</h1>
        {data.description ? <p className={endpoint.lead}>{data.description}</p> : null}
        <p className={styles.meta}>
          {sent ? 'Sent in requests.' : 'Returned in responses.'} {data.closed ? 'Other fields are rejected.' : null}
        </p>

        <section className={endpoint.section}>
          <div className={endpoint.secHead}>
            <h2>Fields</h2>
          </div>
          <Fields fields={data.fields} mode={sent ? 'request' : 'response'} />
        </section>

        {data.usedBy.length > 0 ? (
          <section className={endpoint.section}>
            <div className={endpoint.secHead}>
              <h2>Used by</h2>
            </div>
            <div className={endpoint.more}>
              {endpoints.map((user) => (
                <Link className={endpoint.moreRow} key={user.key} to={`/api/reference/${user.id}`}>
                  <MethodLabel method={user.method} />
                  <span className={endpoint.moreName}>{user.name}</span>
                  <code className={endpoint.morePath}>
                    <PathText path={user.path} />
                  </code>
                </Link>
              ))}
              {types.map((user) => (
                <Link className={endpoint.moreRow} key={user.key} to={`/api/reference/types/${user.slug}`}>
                  <span className={styles.kind}>Type</span>
                  <span className={endpoint.moreName}>{user.name}</span>
                </Link>
              ))}
            </div>
          </section>
        ) : null}
      </article>

      <aside aria-label="Example" className={endpoint.aside}>
        <CodePanel label="Example" tabs={[{id: 'json', label: 'JSON', language: 'json', code: data.example, note: 'Built from the schema.'}]} />
      </aside>
    </div>
  );
}
