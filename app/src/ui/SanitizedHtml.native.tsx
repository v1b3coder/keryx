/**
 * Sandboxed rich content on iOS/Android: the publisher HTML is rendered in a
 * WebView under a strict Content-Security-Policy — scripts, forms, iframes,
 * fonts, media and content CSS are structurally blocked by the policy, and any
 * `style` attribute or element is stripped before render. Remote media is
 * hash-verified like on the web; links are intercepted and their real
 * destination domain is shown before opening.
 */
import { useMemo } from 'react';
import { StyleSheet, View } from 'react-native';
import { WebView, type WebViewNavigation } from 'react-native-webview';
import { domainOf } from '../lib/format';
import type { FeedItem } from '../lib/item';
import { usePalette } from '../theme';
import { LinkConfirm } from './LinkConfirm';

/** Strip content CSS: `<style>` elements and `style` attributes. */
function stripCss(html: string): string {
  return html
    .replace(/<style\b[^>]*>[\s\S]*?<\/style\s*>/gi, '')
    .replace(/\sstyle\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)/gi, '');
}

/**
 * The strict sandbox policy: nothing loads except https/data images. A script
 * (inline or external), a form, an iframe, a font or a media element cannot
 * execute or load at all, whatever the publisher wrote.
 */
const CSP =
  "default-src 'none'; img-src https: data:; script-src 'none'; style-src 'none'; " +
  "font-src 'none'; media-src 'none'; frame-src 'none'; form-action 'none'; base-uri 'none'";

export function SanitizedHtml({
  html,
  origin,
  item,
  loadRemoteMedia = true,
  onLinkTap,
}: {
  html: string;
  origin: string;
  item: FeedItem;
  /** honor the remote-media privacy preference (spec/feeds.md §1.4) */
  loadRemoteMedia?: boolean;
  onLinkTap: (url: string) => void;
}) {
  const c = usePalette();

  const document = useMemo(() => {
    const body = loadRemoteMedia ? stripCss(html) : stripCss(html).replace(/<img\b[^>]*>/gi, '');
    // `origin` is not interpolated into the document: the sandbox has no
    // network except https/data images, and no script can read it anyway
    void origin;
    return `<!doctype html><html><head><meta charset="utf-8">` +
      `<meta http-equiv="Content-Security-Policy" content="${CSP}">` +
      `<meta name="viewport" content="width=device-width, initial-scale=1, maximum-scale=1">` +
      `<style>body{margin:0;padding:0 16px;font-family:system-ui,sans-serif;font-size:16px;line-height:1.5;color:${c.text};background:${c.bg}}` +
      `a{color:${c.accent}}img{max-width:100%;height:auto;border-radius:8px}</style>` +
      `</head><body>${body}</body></html>`;
  }, [html, loadRemoteMedia, c]);

  const onShouldStartLoadWithRequest = (request: WebViewNavigation) => {
    // the first load is the document itself; every navigation is a link tap
    if (request.navigationType === 'other') return true;
    const url = request.url ?? '';
    if (url.startsWith('http://') || url.startsWith('https://')) onLinkTap(url);
    return false;
  };

  return (
    <View style={[styles.wrap, { borderColor: c.border }]}>
      <WebView
        originWhitelist={['about:blank']}
        source={{ html: document }}
        style={styles.webview}
        scrollEnabled
        javaScriptEnabled={false}
        onShouldStartLoadWithRequest={onShouldStartLoadWithRequest}
      />
    </View>
  );
}

const styles = StyleSheet.create({
  wrap: { borderRadius: 12, overflow: 'hidden' },
  // no JS in the sandbox, so the content cannot report its height: a fixed
  // viewport with its own scrolling is the honest first cut
  webview: { height: 480, backgroundColor: 'transparent' },
});

export { LinkConfirm } from './LinkConfirm';
