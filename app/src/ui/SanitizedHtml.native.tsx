/**
 * Sandboxed rich content on iOS/Android: the publisher HTML is rendered in a
 * WebView under a strict Content-Security-Policy — scripts, forms, iframes,
 * fonts, media and content CSS are structurally blocked by the policy, and any
 * `style` attribute or element is stripped before render.
 *
 * Remote media is never loaded by the sandbox itself: every `<img src>` is
 * dropped from the document and re-injected only after `loadImage` verified the
 * bytes against `attachments[].sha256` (spec/feeds.md §1.1), exactly like the
 * web renderer. Unverified bytes are never handed to the image loader.
 *
 * The two helpers below run through the WebView's `injectedJavaScript` (an
 * `evaluateJavascript` call, not a document script), so `script-src 'none'`
 * still holds for everything the publisher wrote.
 */
import { useEffect, useMemo, useState } from 'react';
import { StyleSheet, View } from 'react-native';
import { WebView, type WebViewNavigation } from 'react-native-webview';
import { domainOf } from '../lib/format';
import { loadImage } from '../lib/media';
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
 * Drop every remote `<img>` before the document is rendered. The page re-adds the
 * verified ones as data URLs: the sandbox's own network is closed to everything
 * but https/data images, so a dropped image simply stays a gap.
 */
function withoutImages(html: string): string {
  return html.replace(/<img\b[^>]*>/gi, '');
}

/**
 * The strict sandbox policy: nothing loads except https/data images. A script
 * (inline or external), a form, an iframe, a font or a media element cannot
 * execute or load at all, whatever the publisher wrote.
 */
const CSP =
  "default-src 'none'; img-src https: data:; script-src 'none'; style-src 'none'; " +
  "font-src 'none'; media-src 'none'; frame-src 'none'; form-action 'none'; base-uri 'none'";

/**
 * Runs in the page through `injectedJavaScript` (not subject to the document's
 * CSP): `__keryxImages` swaps the verified data URLs in, `__keryxMeasure`
 * reports the document height so the sandbox has no inner scrollbar. Neither
 * function ever touches the network.
 */
const BRIDGE = `(function () {
  window.__keryxImages = function (sources) {
    for (var i = 0; i < sources.length; i++) {
      var el = document.querySelector('img[data-keryx-src="' + CSS.escape(sources[i].src) + '"]');
      if (el) { el.setAttribute('src', sources[i].dataUrl); el.removeAttribute('data-keryx-src'); }
    }
  };
  window.__keryxMeasure = function () {
    var h = Math.max(document.body.scrollHeight, document.documentElement.scrollHeight);
    if (window.ReactNativeWebView) window.ReactNativeWebView.postMessage(String(h));
  };
  window.__keryxMeasure();
  setTimeout(window.__keryxMeasure, 60);
  setTimeout(window.__keryxMeasure, 400);
})(); true;`;

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
  const [webview, setWebview] = useState<WebView | null>(null);
  const [height, setHeight] = useState(0);

  // the remote sources this article references, in document order
  const sources = useMemo(() => {
    if (!loadRemoteMedia) return [];
    const found: string[] = [];
    const re = /<img\b[^>]*\bsrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))/gi;
    let match: RegExpExecArray | null;
    while ((match = re.exec(html)) !== null) {
      const src = match[1] ?? match[2] ?? match[3] ?? '';
      if (/^https?:\/\//i.test(src) && !found.includes(src)) found.push(src);
    }
    return found;
  }, [html, loadRemoteMedia]);

  const document = useMemo(() => {
    const body = withoutImages(stripCss(html));
    // `origin` is not interpolated into the document: the sandbox has no
    // network except https/data images, and no script can read it anyway
    void origin;
    return (
      `<!doctype html><html><head><meta charset="utf-8">` +
      `<meta http-equiv="Content-Security-Policy" content="${CSP}">` +
      `<meta name="viewport" content="width=device-width, initial-scale=1, maximum-scale=1">` +
      `<style>body{margin:0;padding:0 16px;font-family:system-ui,sans-serif;font-size:16px;line-height:1.5;color:${c.text};background:${c.bg}}` +
      `a{color:${c.accent}}img{max-width:100%;height:auto;border-radius:8px}` +
      `p,li{overflow-wrap:anywhere}</style>` +
      `</head><body>${body}</body></html>`
    );
  }, [html, c]);

  // resolve every image through the hash-verifying loader and hand the verified
  // data URLs to the page; a mismatch leaves the image unrendered
  useEffect(() => {
    let alive = true;
    void (async () => {
      if (!webview || sources.length === 0) return;
      const verified: { src: string; dataUrl: string }[] = [];
      for (const src of sources) {
        const dataUrl = await loadImage(
          src,
          origin,
          item.attachments?.find((a) => a.url === src)?.sha256,
        );
        if (dataUrl) verified.push({ src, dataUrl });
      }
      if (!alive || verified.length === 0) return;
      webview.injectJavaScript(
        `window.__keryxImages(${JSON.stringify(verified)}); window.__keryxMeasure(); true;`,
      );
    })();
    return () => {
      alive = false;
    };
  }, [webview, sources, origin, item]);

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
        ref={setWebview}
        originWhitelist={['about:blank']}
        source={{ html: document }}
        style={[styles.webview, { height: height > 0 ? height : 480 }]}
        scrollEnabled={false}
        javaScriptEnabled
        injectedJavaScript={BRIDGE}
        onMessage={(event) => {
          const next = Number(event.nativeEvent.data);
          if (Number.isFinite(next) && next > 0) setHeight(next);
        }}
        onShouldStartLoadWithRequest={onShouldStartLoadWithRequest}
      />
    </View>
  );
}

const styles = StyleSheet.create({
  wrap: { borderRadius: 12, overflow: 'hidden' },
  webview: { backgroundColor: 'transparent' },
});

export { LinkConfirm };
