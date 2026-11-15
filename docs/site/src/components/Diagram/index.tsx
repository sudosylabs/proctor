import React from 'react';

import diagrams from '@site/src/generated/diagrams.json';
import styles from './styles.module.css';

// The model is produced and checked by docs/site/diagrams/index.mjs; the
// types here describe that normalized shape.
type Arrow = {label?: string; step?: number; accent?: boolean; dashed?: boolean; source?: string};
type Card = {
  type: 'card';
  title: string;
  text?: string;
  items?: string[];
  numbered?: boolean;
  accent?: boolean;
  dashed?: boolean;
  quiet?: boolean;
};
type Group = {
  type: 'group';
  title: string;
  text?: string;
  columns: number;
  accent?: boolean;
  dashed?: boolean;
  items: Card[];
};
type Branch = {
  type: 'branch';
  title?: string;
  footer?: string;
  items: {title: string; text?: string; accent?: boolean}[];
};
type Node = (Card | Group | Branch) & {via: Arrow | null; span: number};
type Connector = Arrow & {from: number; to: number};
type Row = {type: 'row'; items: Node[]; gap: 'normal' | 'wide'; stack: boolean; from?: Connector[]};
type Note = {type: 'note'; text: string; rule: 'none' | 'solid' | 'dashed' | 'bracket'};
type Model = {blocks: (Row | Note)[]};

const models = diagrams as unknown as Record<string, Model>;
const gaps = {normal: 48, wide: 96};

function Inline({value}: {value?: string}): React.ReactNode {
  if (!value) {
    return null;
  }
  return value.split(/(\*\*[^*]+\*\*|`[^`]+`)/).map((part, index) => {
    if (part.startsWith('**')) {
      return <strong key={index}>{part.slice(2, -2)}</strong>;
    }
    if (part.startsWith('`')) {
      return <code key={index}>{part.slice(1, -1)}</code>;
    }
    return part;
  });
}

function ArrowLabel({arrow}: {arrow: Arrow}): React.ReactNode {
  if (!arrow.label && !arrow.step && !arrow.source) {
    return null;
  }
  return (
    <span className={styles.arrowLabel}>
      {arrow.step ? <b className={styles.step}>{arrow.step}</b> : null}
      <Inline value={arrow.label} />
      {arrow.source ? (
        <span className={styles.source}>
          {arrow.label ? ', from ' : 'From '}
          <Inline value={arrow.source} />
        </span>
      ) : null}
    </span>
  );
}

function Head(): React.ReactElement {
  return (
    <svg aria-hidden="true" className={styles.head} viewBox="0 0 8 8">
      <path d="M0 0 L8 4 L0 8 Z" />
    </svg>
  );
}

function tone(arrow: Arrow): string {
  return [arrow.accent ? styles.accentLine : '', arrow.dashed ? styles.dashedLine : ''].join(' ');
}

// The arrow drawn in the gap to the left of a row item.
function SideArrow({arrow, gap}: {arrow: Arrow; gap: number}): React.ReactElement {
  return (
    <span className={`${styles.side} ${tone(arrow)}`} style={{width: gap}}>
      <ArrowLabel arrow={arrow} />
      <span className={styles.sideLine} />
      <Head />
    </span>
  );
}

// The arrow drawn above an item once a row is stacked.
function DownArrow({arrow}: {arrow: Arrow}): React.ReactElement {
  return (
    <div className={`${styles.down} ${tone(arrow)}`}>
      <span className={styles.downLine} />
      <Head />
      <ArrowLabel arrow={arrow} />
    </div>
  );
}

function CardView({card}: {card: Card}): React.ReactElement {
  const List = card.numbered ? 'ol' : 'ul';
  return (
    <div
      className={[
        styles.card,
        card.accent ? styles.accent : '',
        card.dashed ? styles.dashed : '',
        card.quiet ? styles.quiet : '',
      ].join(' ')}>
      <p className={styles.title}>
        <Inline value={card.title} />
      </p>
      {card.text ? (
        <p className={styles.text}>
          <Inline value={card.text} />
        </p>
      ) : null}
      {card.items ? (
        <List className={styles.items}>
          {card.items.map((item, index) => (
            <li key={index}>
              <Inline value={item} />
            </li>
          ))}
        </List>
      ) : null}
    </div>
  );
}

function NodeView({node}: {node: Node}): React.ReactElement {
  if (node.type === 'card') {
    return <CardView card={node} />;
  }
  if (node.type === 'group') {
    return (
      <div
        className={[styles.group, node.accent ? styles.accent : '', node.dashed ? styles.dashed : ''].join(' ')}>
        <p className={styles.groupTitle}>
          <strong>
            <Inline value={node.title} />
          </strong>
          {node.text ? (
            <>
              {' · '}
              <Inline value={node.text} />
            </>
          ) : null}
        </p>
        <div
          className={styles.groupItems}
          data-columns={node.columns}
          style={{gridTemplateColumns: `repeat(${node.columns}, minmax(0, 1fr))`}}>
          {node.items.map((item, index) => (
            <CardView card={item} key={index} />
          ))}
        </div>
      </div>
    );
  }
  return (
    <div className={styles.branch}>
      {node.title ? (
        <p className={styles.branchTitle}>
          <Inline value={node.title} />
        </p>
      ) : null}
      <ul className={styles.outcomes}>
        {node.items.map((item, index) => (
          <li className={item.accent ? styles.accent : undefined} key={index}>
            <Head />
            <p className={styles.title}>
              <Inline value={item.title} />
            </p>
            {item.text ? (
              <p className={styles.text}>
                <Inline value={item.text} />
              </p>
            ) : null}
          </li>
        ))}
      </ul>
      {node.footer ? (
        <p className={styles.footer}>
          <Inline value={node.footer} />
        </p>
      ) : null}
    </div>
  );
}

// Places every item of a row on a grid of equal columns, so the centre of
// any item can be written as a CSS length for the connectors below.
function layout(row: Row): {columns: number; starts: number[]} {
  let next = 0;
  const starts = row.items.map((item) => {
    const start = next;
    next += item.span;
    return start;
  });
  return {columns: next, starts};
}

function centre(row: Row, index: number): string {
  const {columns, starts} = layout(row);
  const gap = gaps[row.gap];
  const start = starts[index];
  const span = row.items[index].span;
  return `calc((100% - ${(columns - 1) * gap}px) * ${(start + span / 2) / columns} + ${gap * (start + (span - 1) / 2)}px)`;
}

function Connectors({above, row}: {above: Row; row: Row}): React.ReactElement {
  const lines = [...(row.from ?? [])].sort((left, right) => Number(!!left.accent) - Number(!!right.accent));
  return (
    <div aria-hidden="true" className={styles.connectors}>
      {lines.map((line, index) => {
        const from = centre(above, line.from - 1);
        const to = centre(row, line.to - 1);
        return (
          <div className={tone(line)} key={index}>
            <span className={styles.stemTop} style={{left: from}} />
            <span
              className={styles.span}
              style={{left: `min(${from}, ${to})`, width: `calc(max(${from}, ${to}) - min(${from}, ${to}))`}}
            />
            <span className={styles.stemBottom} style={{left: to}}>
              <Head />
            </span>
            <span className={styles.connectorLabel} style={{left: to}}>
              <ArrowLabel arrow={line} />
            </span>
          </div>
        );
      })}
    </div>
  );
}

function RowView({row, above}: {row: Row; above: Row | null}): React.ReactElement {
  const {columns, starts} = layout(row);
  const gap = gaps[row.gap];
  // Once the rows stack, a connector reads as coming from the item just above
  // it. Name its real source whenever that is not the case.
  const incoming = new Map(
    (row.from ?? []).map((line) => {
      const adjacent = above !== null && line.to === 1 && line.from === above.items.length;
      const source = above?.items[line.from - 1];
      return [line.to - 1, adjacent || !source ? line : {...line, source: source.title ?? 'above'}];
    }),
  );
  return (
    <>
      {above && row.from && !row.stack ? <Connectors above={above} row={row} /> : null}
      <div
        className={[
          styles.row,
          row.stack ? styles.stack : '',
          // Items of one kind share a height; a mixed row centres each item
          // on the arrow line instead of stretching a card to a tall branch.
          new Set(row.items.map((item) => item.type)).size > 1 ? styles.mixed : '',
        ].join(' ')}
        style={{columnGap: gap, gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))`}}>
        {row.items.map((item, index) => {
          const column = `${starts[index] + 1} / span ${item.span}`;
          const narrow = item.via ?? incoming.get(index) ?? null;
          return (
            <React.Fragment key={index}>
              {narrow ? <DownArrow arrow={narrow} /> : null}
              <div className={styles.cell} style={{gridColumn: column, gridRow: 1}}>
                {item.via ? <SideArrow arrow={item.via} gap={gap} /> : null}
                <NodeView node={item} />
              </div>
            </React.Fragment>
          );
        })}
      </div>
    </>
  );
}

export function hasDiagram(id: string): boolean {
  return id in models;
}

export default function Diagram({id, label}: {id: string; label: string}): React.ReactElement {
  const model = models[id];
  if (!model) {
    throw new Error(`Unknown documentation diagram: ${id}`);
  }
  let above: Row | null = null;
  return (
    <div aria-label={label} className={styles.diagram} role="group">
      {model.blocks.map((block, index) => {
        if (block.type === 'note') {
          above = null;
          return (
            <p className={`${styles.note} ${styles[`rule-${block.rule}`] ?? ''}`} key={index}>
              <Inline value={block.text} />
            </p>
          );
        }
        const view = <RowView above={above} key={index} row={block} />;
        above = block;
        return view;
      })}
    </div>
  );
}
