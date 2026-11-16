import React from 'react';

import assetRegistry from '../../../../public/assets.json';
import Diagram from '@site/src/components/Diagram';
import DocIcon from '@site/src/components/DocIcon';
import styles from './styles.module.css';

type AssetEntry = {
  id: string;
  kind: string;
  alt: string;
  caption: string;
  public_path?: string;
  width?: number;
  height?: number;
};

type GovernedFigureProps = {
  asset: string;
  eager?: boolean;
};

const assetsByID = new Map<string, AssetEntry>(
  (assetRegistry.assets as AssetEntry[]).map((entry) => [entry.id, entry]),
);

export default function GovernedFigure({
  asset,
  eager = false,
}: GovernedFigureProps): React.ReactElement {
  const entry = assetsByID.get(asset);

  if (!entry) {
    throw new Error(`Unknown governed documentation asset: ${asset}`);
  }

  // A diagram is drawn on the page in the site's own type and colours, so it
  // needs no plate and no separate full-size view.
  if (entry.kind === 'diagram') {
    return (
      <figure className={`${styles.figure} ${styles.diagram}`} data-asset-id={entry.id}>
        <Diagram id={entry.id} label={entry.alt} />
        <figcaption className={styles.caption}>{entry.caption}</figcaption>
      </figure>
    );
  }

  return (
    <figure className={styles.figure} data-asset-id={entry.id}>
      <div className={styles.plate}>
        <img
          alt={entry.alt}
          className={styles.image}
          decoding="async"
          fetchPriority={eager ? 'high' : undefined}
          height={entry.height}
          loading={eager ? 'eager' : 'lazy'}
          src={entry.public_path}
          width={entry.width}
        />
      </div>
      <figcaption className={styles.caption}>
        <span>{entry.caption}</span>
        <a
          aria-label={`Open ${entry.kind} at full size in a new tab`}
          className={styles.fullSize}
          href={entry.public_path}
          rel="noopener noreferrer"
          target="_blank">
          Open full size <DocIcon name="external" />
        </a>
      </figcaption>
    </figure>
  );
}
