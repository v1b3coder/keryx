/**
 * Company detail: branded header (logo + name + origin), one-way feed of FULL
 * articles (big square picture, title, date/tags, content — no separate detail
 * view), channel toggles + filter sheet, suspension and rebranding states per
 * spec/core.md §2, §4.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ActivityIndicator, Alert as RNAlert, FlatList, Image, Modal, PanResponder, Pressable, ScrollView, StyleSheet, Text, View, type ViewToken } from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import type { ChannelState, CompanyRecord, StoredItem } from '../lib/store';
import { formatDate, formatDateTime, matchesFilter } from '../lib/format';
import { loadImage } from '../lib/media';
import { attachmentSha, bytesMatchSha, type FeedItem } from '../lib/item';
import { openExternal } from '../lib/push';
import { CompanyLogo } from './CompanyLogo';
import { SanitizedHtml } from './SanitizedHtml';
import { LinkConfirm } from './LinkConfirm';
import { NotificationBanner } from './NotificationBanner';
import { BuildStamp } from './BuildStamp';
import { useApp } from '../state';
import { Alert, Body, Button, Card, Chip, IconButton, Mono, Screen, Small, Title, Toggle } from './components';
import { radius, size, spacing, type, usePalette } from '../theme';

/**
 * Open an attachment after verifying its `sha256` when present
 * (spec/feeds.md §1.1: verify before rendering, opening, or saving;
 * a mismatch makes the resource unavailable).
 */
async function openAttachment(url: string, item: FeedItem) {
  const sha = attachmentSha(item, url);
  if (sha === null) return; // malformed pin — never opened
  if (sha) {
    let ok = false;
    try {
      const res = await fetch(url);
      if (res.ok) ok = bytesMatchSha(new Uint8Array(await res.arrayBuffer()), sha);
    } catch {
      ok = false;
    }
    if (!ok) return; // resource unavailable — never opened
  }
  await openExternal(url);
}

export function CompanyView({
  company,
  items,
  onBack,
  onRepair,
  onAdd,
}: {
  company: CompanyRecord;
  items: StoredItem[];
  onBack: () => void;
  /** re-pair flow for a company_name change (scan a fresh QR) */
  onRepair: (origin: string) => void;
  /** add another company (single-source shortcut: no contacts list yet) */
  onAdd: () => void;
}) {
  const c = usePalette();
  const { actions, companies, syncing, notification, freshTest } = useApp();
  const [showSettings, setShowSettings] = useState(false);
  const [pendingLink, setPendingLink] = useState<{ url: string; item: FeedItem } | null>(null);

  const followed = new Set(company.channels.filter((ch) => ch.followed).map((ch) => ch.name));
  const visible = useMemo(() => {
    return items
      .filter((i) => {
        if (i.isPrivate) return true;
        if (!followed.has(i.channel)) return false;
        return matchesFilter(i.item, company.prefs);
      })
      .sort((a, b) => {
        const ta = Date.parse(a.published);
        const tb = Date.parse(b.published);
        if (!Number.isNaN(ta) && !Number.isNaN(tb)) return tb - ta;
        if (!Number.isNaN(ta)) return -1;
        if (!Number.isNaN(tb)) return 1;
        return (b.feedIndex ?? 0) - (a.feedIndex ?? 0);
      });
  }, [items, followed, company.prefs]);

  const anyFollowed = followed.size > 0;

  // mark read when an article scrolls into view (no detail view anymore)
  const onViewableItemsChanged = useCallback(
    ({ viewableItems }: { viewableItems: ViewToken[] }) => {
      for (const entry of viewableItems) {
        const stored = entry.item as StoredItem;
        if (!stored.read) {
          const feedKey = stored.isPrivate ? `private:${stored.feedUrl}` : `public:${stored.channel}`;
          void actions.markRead(company.origin, feedKey, stored.item.id!, true);
        }
      }
    },
    [actions, company.origin],
  );
  const viewabilityConfig = useRef({ itemVisiblePercentThreshold: 60 }).current;

  // --- suspension (spec/core.md §4): warning + Remove only, no re-pair ----
  if (company.status === 'suspended') {
    return (
      <Screen>
        <ScrollView contentContainerStyle={styles.pad}>
          <Alert danger>
            <Title>Messages are not shown</Title>
            <Body>
              This company's identity changed. This can mean the company's website or signing keys
              were compromised.
            </Body>
            <Mono>{company.origin}</Mono>
          </Alert>
          <Button title="Remove company" variant="danger" onPress={() => void actions.removeCompany(company.origin)} />
          <Button title="Back" variant="ghost" onPress={onBack} />
        </ScrollView>
      </Screen>
    );
  }

  // --- rebranding (spec/core.md §2): company_name changed → re-pair required
  if (company.status === 'rebrand' || company.rebrandPending) {
    return (
      <Screen>
        <ScrollView contentContainerStyle={styles.pad}>
          <Alert danger>
            <Title>This company changed its name</Title>
            <Body>
              A company's name can only change after you confirm it again. Scan a fresh QR code
              from the company to continue receiving its messages.
            </Body>
            <Mono>{company.origin}</Mono>
          </Alert>
          <Button title="Scan a new QR code" onPress={() => onRepair(company.origin)} />
          <Button title="Remove company" variant="danger" onPress={() => void actions.removeCompany(company.origin)} />
        </ScrollView>
      </Screen>
    );
  }

  return (
    <Screen>
      <View style={[styles.appbar, { borderBottomColor: c.border }]}>
        <View style={styles.appbarInner}>
          {companies.length > 1 ? (
            <IconButton
              icon={<Ionicons name="chevron-back" size={24} color={c.text} />}
              accessibilityLabel="Back"
              onPress={onBack}
            />
          ) : null}
          <View style={styles.companybar}>
            <CompanyLogo
              url={company.targets.signed.custom?.logo}
              origin={company.origin}
              expectedSha={company.targets.signed.custom?.logo_sha256}
              size={size.logo}
            />
            <View style={styles.companybarText}>
              <Text numberOfLines={1} style={[styles.companybarName, { color: c.text }]}>
                {company.targets.signed.custom?.company_name ?? company.origin}
              </Text>
              <Mono numberOfLines={1} style={styles.companybarOrigin}>
                {company.origin}
              </Mono>
            </View>
          </View>
          <IconButton
            icon={<Ionicons name="refresh" size={22} color={c.text} />}
            accessibilityLabel="Refresh"
            busy={syncing}
            onPress={() => void actions.syncCompanyNow(company.origin)}
          />
          <IconButton
            icon={<Ionicons name="settings-outline" size={22} color={c.text} />}
            accessibilityLabel="Settings"
            onPress={() => setShowSettings(true)}
          />
          {companies.length === 1 ? (
            <IconButton
              icon={<Ionicons name="add" size={26} color={c.text} />}
              accessibilityLabel="Add company"
              onPress={onAdd}
            />
          ) : null}
        </View>
      </View>

      <View style={styles.bannerWrap}>
        <NotificationBanner
          state={notification}
          freshTest={freshTest}
          onEnable={() => void actions.enableNotifications()}
          onCheck={() => void actions.checkNotifications()}
          onRetry={() => void actions.runNotificationSelfTest()}
        />
      </View>

      {company.logoChangePending ? (
        <View style={styles.bannerWrap}>
          <Alert>
            <Body>The company updated its logo.</Body>
            <Button title="Got it" variant="secondary" onPress={() => void actions.acknowledgeLogo(company.origin)} />
          </Alert>
        </View>
      ) : null}

      {company.lastSyncErrors && company.lastSyncErrors.length > 0 ? (
        <View style={styles.bannerWrap}>
          <Small muted>{company.lastSyncErrors[0]}</Small>
        </View>
      ) : null}

      <FlatList
        data={visible}
        keyExtractor={(stored) => stored.id}
        contentContainerStyle={styles.feed}
        viewabilityConfig={viewabilityConfig}
        onViewableItemsChanged={onViewableItemsChanged}
        ListEmptyComponent={
          <View style={styles.empty}>
            <Title>{anyFollowed ? 'No messages yet' : 'No channels yet'}</Title>
            <Small muted>
              {anyFollowed
                ? 'Messages appear here as soon as the company publishes.'
                : 'Open settings to follow a channel from this company.'}
            </Small>
          </View>
        }
        ListFooterComponent={
          visible.length > 0 ? (
            <View style={[styles.footer, { borderTopColor: c.border }]}>
              <Ionicons name="lock-closed" size={15} color={c.text2} style={styles.footerIcon} />
              <Small muted>This channel will never ask you for a password, seed, or code.</Small>
            </View>
          ) : null
        }
        renderItem={({ item: stored }) => (
          <FeedArticle
            company={company}
            stored={stored}
            loadRemoteMedia={company.prefs.loadRemoteMedia}
            onLinkTap={(url) => setPendingLink({ url, item: stored.item })}
          />
        )}
      />

      {showSettings ? <SettingsSheet company={company} items={items} onClose={() => setShowSettings(false)} /> : null}

      {pendingLink ? (
        <LinkConfirm
          url={pendingLink.url}
          onConfirm={() => {
            void openAttachment(pendingLink.url, pendingLink.item);
            setPendingLink(null);
          }}
          onCancel={() => setPendingLink(null)}
        />
      ) : null}
    </Screen>
  );
}

/** One full article in the feed: big square picture, title, date/tags, content. */
function FeedArticle({
  company,
  stored,
  loadRemoteMedia,
  onLinkTap,
}: {
  company: CompanyRecord;
  stored: StoredItem;
  /** honor the remote-media privacy preference (spec/feeds.md §1.4) */
  loadRemoteMedia: boolean;
  onLinkTap: (url: string) => void;
}) {
  const c = usePalette();
  const [img, setImg] = useState<string | null>(null);
  const [showTime, setShowTime] = useState(false);
  const item = stored.item;

  useEffect(() => {
    let alive = true;
    const url = item.image;
    setImg(null);
    if (!url) return;
    if (!url.startsWith('data:') && !loadRemoteMedia) return; // remote media disabled
    // a linked image is hash-pinned by image_sha256 (spec/feeds.md §1.1)
    void loadImage(url, stored.origin, item.image_sha256).then((loaded) => {
      if (alive) setImg(loaded);
    });
    return () => {
      alive = false;
    };
  }, [item.image, item.image_sha256, stored.origin, loadRemoteMedia]);

  const published = item.date_published ?? '';
  const date = published ? formatDate(published) : '';
  const dateTime = published ? formatDateTime(published) : '';

  return (
    <Card style={styles.article}>
      {img ? <Image source={{ uri: img }} style={styles.articleImg} /> : null}
      <View style={styles.articleBody}>
        <Text style={[styles.articleTitle, { color: c.text }]}>{item.title ?? 'Untitled'}</Text>
        <View style={styles.meta}>
          {date ? (
            <Chip label={showTime ? dateTime : date} onPress={() => setShowTime((v) => !v)} />
          ) : null}
          {stored.updated ? <Chip label="Updated" on /> : null}
          {(item.tags ?? []).slice(0, 4).map((tag) => (
            <Chip key={tag} label={tag} />
          ))}
        </View>
        <SanitizedHtml
          html={item.content_html ?? ''}
          origin={company.origin}
          item={item}
          loadRemoteMedia={company.prefs.loadRemoteMedia}
          onLinkTap={onLinkTap}
        />
      </View>
    </Card>
  );
}
function SettingsSheet({
  company,
  items,
  onClose,
}: {
  company: CompanyRecord;
  items: StoredItem[];
  onClose: () => void;
}) {
  const c = usePalette();
  const { actions } = useApp();
  const [languages, setLanguages] = useState<string[]>(company.prefs.languages);
  const [tags, setTags] = useState<string[]>(company.prefs.tags);
  const companyItems = items.filter((i) => i.origin === company.origin);

  const allLanguages = useMemo(
    () => [...new Set(companyItems.map((i) => i.item.language).filter((l): l is string => !!l))].sort(),
    [companyItems],
  );
  const allTags = useMemo(
    () => [...new Set(companyItems.flatMap((i) => i.item.tags ?? []))].sort(),
    [companyItems],
  );

  function toggleLanguage(lang: string) {
    const next = languages.includes(lang) ? languages.filter((l) => l !== lang) : [...languages, lang];
    setLanguages(next);
    void actions.setPrefs(company.origin, { languages: next });
  }

  function toggleTag(tag: string) {
    const next = tags.includes(tag) ? tags.filter((t) => t !== tag) : [...tags, tag];
    setTags(next);
    void actions.setPrefs(company.origin, { tags: next });
  }

  return (
    <Modal visible transparent animationType="slide" onRequestClose={onClose}>
      {/* the backdrop tap dismisses the sheet, like the reference .sheet-backdrop;
          the sheet itself swallows the tap so an inner tap never closes it */}
      <Pressable style={styles.backdrop} accessibilityLabel="Close settings" onPress={onClose}>
        <Pressable
          style={[styles.sheet, { backgroundColor: c.surface, borderColor: c.border }]}
          onPress={(e) => e.stopPropagation()}
        >
          <SheetHandle onClose={onClose} color={c.border} />
          <ScrollView contentContainerStyle={styles.sheetBody}>
            <Title>Settings</Title>

            <Small>Channels</Small>
            {company.channels.map((channel) => (
              <ChannelToggle
                key={channel.name}
                channel={channel}
                onToggle={(followed) => void actions.toggleChannel(company.origin, channel.name, followed)}
              />
            ))}

            {company.privateFeeds.length > 0 ? (
              <>
                <Small>Orders</Small>
                {company.privateFeeds.map((f) => (
                  <View key={f.url}>
                    <Body>{f.displayName ?? 'Delivery'}</Body>
                    <Small muted>
                      {f.closed ? 'Finished' : f.expires ? `Open until ${f.expires.slice(0, 10)}` : 'Open'}
                    </Small>
                  </View>
                ))}
              </>
            ) : null}

            <Small>Language</Small>
            {allLanguages.length === 0 ? (
              <Small muted>No messages yet.</Small>
            ) : (
              <View style={styles.chips}>
                {allLanguages.map((lang) => (
                  <Chip key={lang} label={lang} on={languages.includes(lang)} onPress={() => toggleLanguage(lang)} />
                ))}
              </View>
            )}

            <Small>Tags</Small>
            {allTags.length === 0 ? (
              <Small muted>No messages yet.</Small>
            ) : (
              <View style={styles.chips}>
                {allTags.map((tag) => (
                  <Chip key={tag} label={tag} on={tags.includes(tag)} onPress={() => toggleTag(tag)} />
                ))}
              </View>
            )}

            <Small>Remote media</Small>
            <Chip
              label={company.prefs.loadRemoteMedia ? 'Load images from the web' : 'Images off (privacy)'}
              on={company.prefs.loadRemoteMedia}
              onPress={() =>
                void actions.setPrefs(company.origin, { loadRemoteMedia: !company.prefs.loadRemoteMedia })
              }
            />

            <Button title="Close" variant="secondary" onPress={onClose} />
            <Button
              title="Remove company"
              variant="danger"
              onPress={() => {
                // the destructive action deletes the company, its items and its
                // cached media: never one tap away
                RNAlert.alert(
                  'Remove company',
                  `Remove ${company.origin}? All saved messages are deleted from this device.`,
                  [
                    { text: 'Cancel', style: 'cancel' },
                    {
                      text: 'Remove',
                      style: 'destructive',
                      onPress: () => {
                        void actions.removeCompany(company.origin);
                        onClose();
                      },
                    },
                  ],
                );
              }}
            />
            <BuildStamp />
          </ScrollView>
        </Pressable>
      </Pressable>
    </Modal>
  );
}

/**
 * The sheet's grab handle: a downward swipe past the threshold closes the
 * sheet, mirroring the reference backdrop gesture. PanResponder is used because
 * a plain responder reports only the release, not the travel distance.
 */
function SheetHandle({ onClose, color }: { onClose: () => void; color: string }) {
  const pan = useMemo(
    () =>
      PanResponder.create({
        onStartShouldSetPanResponder: () => true,
        onMoveShouldSetPanResponder: (_evt, gesture) => Math.abs(gesture.dy) > 4,
        onPanResponderRelease: (_evt, gesture) => {
          if (gesture.dy > 60) onClose();
        },
      }),
    [onClose],
  );
  return (
    <View style={styles.handleWrap} {...pan.panHandlers}>
      <View accessibilityLabel="Drag down to close" style={[styles.sheetHandle, { backgroundColor: color }]} />
    </View>
  );
}

function ChannelToggle({
  channel,
  onToggle,
}: {
  channel: ChannelState;
  onToggle: (followed: boolean) => void;
}) {
  const c = usePalette();
  return (
    <Pressable
      accessibilityRole="switch"
      accessibilityLabel={channel.displayName}
      accessibilityState={{ checked: channel.followed }}
      onPress={() => onToggle(!channel.followed)}
      style={[styles.channelRow, { borderBottomColor: c.border }]}
    >
      <View style={{ flex: 1, minWidth: 0 }}>
        <Body>
          {channel.displayName}
          {channel.isNew ? '  · New' : ''}
        </Body>
        {channel.description ? <Small muted>{channel.description}</Small> : null}
      </View>
      <Toggle on={channel.followed} />
    </Pressable>
  );
}

const styles = StyleSheet.create({
  pad: { flex: 1, padding: spacing(2.5), gap: spacing(1.5) },
  // the app bar is full-bleed; its inner row carries the 640px measure and the
  // 10/16 padding of the reference .appbar-inner, so the 40pt icon buttons sit
  // flush with the page edge and the company name keeps its column
  appbar: { borderBottomWidth: StyleSheet.hairlineWidth },
  appbarInner: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing(0.75),
    paddingHorizontal: spacing(2),
    paddingVertical: spacing(1),
    minHeight: 60,
  },
  companybar: { flexDirection: 'row', alignItems: 'center', gap: spacing(0.75), flex: 1, minWidth: 0 },
  companybarText: { flexShrink: 1, minWidth: 0 },
  companybarName: { fontSize: type.company, fontWeight: '600', lineHeight: 21 },
  companybarOrigin: { fontSize: type.origin, lineHeight: 16 },
  bannerWrap: { paddingHorizontal: spacing(2.5), paddingVertical: spacing(0.5) },
  feed: { paddingBottom: spacing(2) },
  empty: { alignItems: 'center', gap: spacing(1), paddingVertical: spacing(6), paddingHorizontal: spacing(3) },
  footer: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    gap: spacing(1),
    marginTop: spacing(3.5),
    marginHorizontal: spacing(2.5),
    paddingTop: spacing(2),
    borderTopWidth: StyleSheet.hairlineWidth,
  },
  footerIcon: { marginTop: 2 },
  // .article-card: 20px side padding, 0 bottom (the border is the separator),
  // and no vertical margin between cards
  article: {
    marginVertical: 0,
    paddingHorizontal: spacing(2.5),
    paddingTop: spacing(2.5),
    paddingBottom: 0,
    borderRadius: 0,
    borderWidth: 0,
    borderBottomWidth: StyleSheet.hairlineWidth,
    overflow: 'visible',
  },
  articleImg: { width: '100%', aspectRatio: 1, borderRadius: radius.card },
  // .article-card-body: padding-top 14px; the title/meta/content spacing is
  // the reference's own margins, not a uniform flex gap
  articleBody: { paddingTop: spacing(1.75) },
  articleTitle: {
    fontSize: type.article,
    fontWeight: '700',
    lineHeight: 30,
    letterSpacing: -0.24,
    marginBottom: spacing(1),
  },
  meta: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: spacing(1),
    alignItems: 'center',
    marginBottom: spacing(1.75),
  },
  backdrop: { flex: 1, justifyContent: 'flex-end', backgroundColor: 'rgba(0,0,0,0.35)' },
  sheetHandle: { alignSelf: 'center', width: 36, height: 4, borderRadius: 2 },
  handleWrap: { alignSelf: 'stretch', alignItems: 'center', paddingVertical: spacing(1) },
  sheet: {
    maxHeight: '86%',
    borderTopLeftRadius: 20,
    borderTopRightRadius: 20,
    paddingTop: spacing(2.5),
  },
  sheetBody: { paddingHorizontal: spacing(2.5), paddingBottom: spacing(3), gap: spacing(1) },
  chips: { flexDirection: 'row', flexWrap: 'wrap', gap: spacing(1) },
  channelRow: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing(1.5),
    paddingVertical: spacing(1.5),
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
});
