/**
 * Company logo: an inline data URL is self-authenticated by the metadata
 * signature; a linked logo is hash-pinned by `logo_sha256` and shown only
 * after the bytes verify. Anything else is a neutral placeholder — the company
 * identity is never shown from unverified bytes (spec/repository.md §2).
 */
import { useEffect, useState } from 'react';
import { Image, View, StyleSheet, Text } from 'react-native';
import { loadImage } from '../lib/media';
import { usePalette } from '../theme';

export function CompanyLogo({
  url,
  origin,
  expectedSha,
  size = 32,
}: {
  url?: string;
  origin: string;
  expectedSha?: string;
  size?: number;
}) {
  const c = usePalette();
  const [src, setSrc] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    setSrc(null);
    if (!url) return;
    if (!url.startsWith('data:') && !expectedSha) return;
    void loadImage(url, origin, expectedSha).then((loaded) => {
      if (alive) setSrc(loaded);
    });
    return () => {
      alive = false;
    };
  }, [url, origin, expectedSha]);

  const style = { width: size, height: size, borderRadius: size / 6 };
  if (src) return <Image source={{ uri: src }} style={style} accessibilityIgnoresInvertColors />;
  return (
    <View style={[style, styles.placeholder, { backgroundColor: c.surface2, borderColor: c.border }]}>
      <Text style={{ color: c.text2, fontSize: size / 2.5, fontWeight: '700' }}>
        {(origin.replace(/^https?:\/\//, '')[0] ?? '?').toUpperCase()}
      </Text>
    </View>
  );
}

const styles = StyleSheet.create({
  placeholder: {
    alignItems: 'center',
    justifyContent: 'center',
    borderWidth: StyleSheet.hairlineWidth,
  },
});
