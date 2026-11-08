import type { HTMLAttributes, ReactNode } from "react";

import styles from "./FactList.module.css";

export type FactListProps = HTMLAttributes<HTMLDListElement>;

/** A description list of label/value facts separated by hairline rules. */
export function FactList({ className, ...props }: FactListProps) {
  return (
    <dl
      {...props}
      className={className === undefined ? styles.list : `${styles.list} ${className}`}
      data-proctor-fact-list=""
    />
  );
}

export interface FactProps {
  children: ReactNode;
  label: ReactNode;
  /** Sets the value in the data face, for codes and identifiers. */
  mono?: boolean;
}

/** One labelled fact. Use only as a direct child of `FactList`. */
export function Fact({ children, label, mono = false }: FactProps) {
  return (
    <div className={styles.fact}>
      <dt className={styles.label}>{label}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>
        {children}
      </dd>
    </div>
  );
}
