/**
 * Company logo with integrity check: `custom.logo_sha256` when present
 * (spec/repository.md §2) — on mismatch a neutral placeholder is shown and
 * nothing else is affected.
 */

import { useEffect, useState } from 'react';
import { loadImage } from '../lib/media';

export function CompanyLogo({
  url,
  origin,
  expectedSha,
  size,
  className,
}: {
  url?: string;
  origin: string;
  expectedSha?: string;
  size?: number;
  className?: string;
}) {
  const [src, setSrc] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    if (!url) {
      setSrc(null);
      return;
    }
    void loadImage(url, origin, expectedSha).then((s) => {
      if (alive) setSrc(s);
    });
    return () => {
      alive = false;
    };
  }, [url, origin, expectedSha]);

  if (!src) {
    return (
      <div
        className={className ?? 'companybar-logo'}
        style={{ width: size, height: size, borderRadius: size ? 12 : undefined }}
        aria-hidden
      />
    );
  }
  return (
    <img
      className={className ?? 'companybar-logo'}
      src={src}
      alt=""
      style={{ width: size, height: size, borderRadius: size ? 12 : undefined }}
    />
  );
}
