/**
 * Keryx app — design tokens for React Native, derived from the web
 * styles.css (GRAPHICAL_DESIGN.md): electric blue accent, near-black text,
 * 8px spacing base, pill controls, system text sizes.
 *
 * Text sizes are in points and follow the OS text-size setting through the
 * platform's own dynamic type (the native equivalent of the web rem scale):
 * no in-app size control.
 */
import { useColorScheme } from 'react-native';

export interface Palette {
  bg: string;
  surface: string;
  surface2: string;
  text: string;
  text2: string;
  accent: string;
  accentSoft: string;
  onAccent: string;
  border: string;
  danger: string;
  dangerSoft: string;
  okSoft: string;
}

const light: Palette = {
  bg: '#ffffff',
  surface: '#ffffff',
  surface2: '#f4f4f6',
  text: '#313131',
  text2: '#6e6e73',
  accent: '#0000ee',
  accentSoft: 'rgba(0, 0, 238, 0.08)',
  onAccent: '#ffffff',
  border: 'rgba(49, 49, 49, 0.14)',
  danger: '#d70015',
  dangerSoft: 'rgba(215, 0, 21, 0.08)',
  okSoft: 'rgba(26, 127, 55, 0.1)',
};

const dark: Palette = {
  bg: '#101013',
  surface: '#16161a',
  surface2: '#1f1f24',
  text: '#f2f2f4',
  text2: '#a1a1a8',
  accent: '#6b6bff',
  accentSoft: 'rgba(107, 107, 255, 0.16)',
  onAccent: '#ffffff',
  border: 'rgba(242, 242, 244, 0.16)',
  danger: '#ff453a',
  dangerSoft: 'rgba(255, 69, 58, 0.14)',
  okSoft: 'rgba(74, 222, 128, 0.14)',
};

export function usePalette(): Palette {
  return useColorScheme() === 'dark' ? dark : light;
}

export const spacing = (n: number): number => n * 8;

/** The shared type scale, in points; dynamic type scales it per the OS. */
export const type = {
  title: 28,
  section: 20,
  body: 16,
  small: 13,
  mono: 12,
} as const;

export const radius = { pill: 50, card: 16, control: 12 } as const;
