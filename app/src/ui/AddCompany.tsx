/**
 * Pairing flow: input (scan/paste) → confirm the origin (the only human
 * step — nothing is fetched before it, no name/logo shown) → consent summary
 * (company in the publisher's own signed words) → subscribe.
 */
import { useEffect, useState } from 'react';
import * as Clipboard from 'expo-clipboard';
import { Platform, Pressable, StyleSheet, Text, TextInput, View } from 'react-native';
import { parseJoinUrl, type JoinPayload } from '../lib/payload';
import { buildPairingOffer, createCompanyFromOffer, type PairingOffer } from '../lib/pair';
import { syncCompany } from '../lib/sync';
import { getItems, deleteItems } from '../lib/store';
import { nativeNotificationGranted, permissionState, type SelfTestResult } from '../lib/notify';
import { wakeupsCurrent, openExternal, NTFY_INSTALL_URL } from '../lib/push';
import { CompanyLogo } from './CompanyLogo';
import { useApp } from '../state';
import { Alert, Body, Button, Card, Mono, Screen, Small, Spinner, Title, Toggle } from './components';
import { ScanScreen } from './ScanScreen';
import { radius, spacing, type, usePalette } from '../theme';

type Step =
  | { t: 'input'; error?: string }
  | { t: 'scan' }
  | { t: 'confirm'; origin: string; joinUrl: string; payload: JoinPayload }
  | { t: 'loading'; origin: string; joinUrl: string; payload: JoinPayload }
  | { t: 'consent'; offer: PairingOffer }
  | { t: 'notifications'; origin: string }
  | { t: 'error'; message: string };

export function AddCompany({
  onDone,
  onCancel,
  repairOrigin,
  initialUrl,
}: {
  onDone: (origin: string) => void;
  onCancel: () => void;
  /** set when re-pairing a company whose name changed (spec/core.md §2) */
  repairOrigin?: string;
  /** set when opened from a deep link — pairing starts without the input screen */
  initialUrl?: string;
}) {
  const [step, setStep] = useState<Step>({ t: 'input' });
  const [pasteValue, setPasteValue] = useState('');
  const c = usePalette();
  const { actions, companies } = useApp();

  // Deep link: go straight to the origin confirmation.
  useEffect(() => {
    if (initialUrl) startPairing(initialUrl);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function startPairing(input: string) {
    try {
      const parsed = parseJoinUrl(input);
      setStep({ t: 'confirm', origin: parsed.origin, joinUrl: parsed.joinUrl, payload: parsed.payload });
    } catch (err) {
      const e = err as Error & { needsNewerApp?: boolean };
      setStep({
        t: 'input',
        error: e.needsNewerApp ? e.message : 'This link is not a valid company link.',
      });
    }
  }

  async function pasteFromClipboard() {
    const text = (await Clipboard.getStringAsync()).trim();
    if (text) startPairing(text);
  }

  async function confirmOrigin() {
    if (step.t !== 'confirm') return;
    const { origin, joinUrl, payload } = step;
    setStep({ t: 'loading', origin, joinUrl, payload });
    try {
      const offer = await buildPairingOffer(origin, joinUrl, payload);
      setStep({ t: 'consent', offer });
    } catch (err) {
      setStep({ t: 'error', message: err instanceof Error ? err.message : 'Could not reach the company.' });
    }
  }

  async function subscribe(followed: string[]) {
    if (step.t !== 'consent') return;
    if (repairOrigin && step.offer.origin !== repairOrigin) {
      setStep({
        t: 'error',
        message: 'This QR code is for a different company. Use the QR from the company you already follow.',
      });
      return;
    }
    const firstCompany = companies.length === 0;
    const company = createCompanyFromOffer(step.offer, followed);
    try {
      // re-pairing keeps the cached items (same origin key); read states preserved
      const existing = new Map((await getItems(company.origin)).map((i) => [i.id, i]));
      const outcome = await syncCompany(company, fetch, existing);
      if (outcome.toDelete.length > 0) await deleteItems(outcome.toDelete);
      if (repairOrigin) {
        await actions.rePairCompany(company.origin, outcome.company, outcome.toPut);
      } else {
        await actions.saveCompany(outcome.company, outcome.toPut);
      }
    } catch {
      if (repairOrigin) {
        await actions.rePairCompany(company.origin, company);
      } else {
        await actions.saveCompany(company);
      }
    }
    if (repairOrigin) {
      onDone(company.origin);
      return;
    }
    // a later company with permission already granted and the registration
    // current skips the screen and self-tests silently: no prompt is possible
    if (!firstCompany) {
      const permissionOk =
        Platform.OS !== 'web' ? await nativeNotificationGranted() : permissionState() === 'granted';
      if (permissionOk && (await wakeupsCurrent(company))) {
        await actions.runNotificationSelfTest();
        onDone(company.origin);
        return;
      }
    }
    setStep({ t: 'notifications', origin: company.origin });
  }

  if (step.t === 'scan') {
    return (
      <ScanScreen
        onScan={(text) => startPairing(text)}
        onCancel={() => setStep({ t: 'input' })}
      />
    );
  }

  if (step.t === 'input') {
    return (
      <Screen>
        <View style={styles.pad}>
          <Title>Add a company</Title>
          <Body muted style={styles.gap}>
            Scan the QR code a company printed or showed you. Its announcements will appear here, verified.
          </Body>
          <TextInput
            style={[styles.input, { color: c.text, borderColor: c.border, backgroundColor: c.surface2 }]}
            placeholder="Paste the company link or domain"
            placeholderTextColor={c.text2}
            autoCapitalize="none"
            autoCorrect={false}
            value={pasteValue}
            onChangeText={setPasteValue}
            onSubmitEditing={() => pasteValue.trim() && startPairing(pasteValue)}
          />
          <Button title="Continue" disabled={!pasteValue.trim()} onPress={() => startPairing(pasteValue)} />
          <Button title="Paste from clipboard" variant="secondary" onPress={() => void pasteFromClipboard()} />
          <Button title="Scan QR code" onPress={() => setStep({ t: 'scan' })} />
          <Button title="Back" variant="ghost" onPress={onCancel} />
          {step.error ? (
            <Alert danger>
              <Small>{step.error}</Small>
            </Alert>
          ) : null}
        </View>
      </Screen>
    );
  }

  if (step.t === 'confirm') {
    return (
      <Screen>
        <View style={styles.pad}>
          <Title>Confirm this website</Title>
          <Body muted>You are subscribing to messages from:</Body>
          <Card style={styles.gap}>
            <Mono style={{ fontSize: type.body }}>{step.origin}</Mono>
          </Card>
          <Small muted>
            Check that this is the company's real website address. This is the only thing you confirm.
            Everything after this is verified automatically.
          </Small>
          <Button title="Continue" onPress={() => void confirmOrigin()} />
          <Button title="Cancel" variant="secondary" onPress={onCancel} />
        </View>
      </Screen>
    );
  }

  if (step.t === 'loading') {
    return (
      <Screen>
        <Spinner label="Checking the company's signed metadata…" />
      </Screen>
    );
  }

  if (step.t === 'consent') {
    return (
      <ConsentScreen
        offer={step.offer}
        onSubscribe={subscribe}
        // Back returns to the origin confirmation, not the paste/scan input: the
        // user confirms what they already fetched instead of starting over
        onBack={() =>
          setStep({
            t: 'confirm',
            origin: step.offer.origin,
            joinUrl: step.offer.joinUrl,
            payload: parseJoinUrl(step.offer.joinUrl).payload,
          })
        }
      />
    );
  }

  if (step.t === 'notifications') {
    return (
      <NotificationsScreen
        onEnable={() => actions.enableNotifications()}
        onDone={() => onDone(step.origin)}
      />
    );
  }

  return (
    <Screen>
      <View style={styles.pad}>
        <Alert danger>
          <Body>Could not add this company</Body>
          <Small>{step.message}</Small>
        </Alert>
        <Button title="Back" onPress={onCancel} />
      </View>
    </Screen>
  );
}

function ConsentScreen({
  offer,
  onSubscribe,
  onBack,
}: {
  offer: PairingOffer;
  onSubscribe: (followed: string[]) => Promise<void>;
  onBack: () => void;
}) {
  const c = usePalette();
  const [selected, setSelected] = useState<Set<string>>(
    () => new Set(offer.channels.filter((ch) => ch.suggested).map((ch) => ch.name)),
  );
  const [busy, setBusy] = useState(false);

  function toggle(name: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  }

  return (
    <Screen>
      <View style={styles.pad}>
        <View style={styles.identity}>
          <CompanyLogo url={offer.logo} origin={offer.origin} expectedSha={offer.logoSHA256} size={56} />
          <View style={{ flex: 1, minWidth: 0 }}>
            <Text style={[styles.section, { color: c.text }]}>{offer.companyName}</Text>
            <Mono>{offer.origin}</Mono>
          </View>
        </View>

        <Text style={[styles.section, { color: c.text }]}>Choose what to follow</Text>
        <Small muted>Tap to subscribe to each channel. You can change this later.</Small>

        {offer.channels.map((ch) => (
          <Pressable
            key={ch.name}
            accessibilityRole="switch"
            accessibilityLabel={ch.displayName}
            accessibilityState={{ checked: selected.has(ch.name) }}
            onPress={() => toggle(ch.name)}
            style={[styles.row, { borderBottomColor: c.border }]}
          >
            <View style={{ flex: 1, minWidth: 0 }}>
              <Body>{ch.displayName}</Body>
              {ch.description ? <Small muted>{ch.description}</Small> : null}
            </View>
            <Toggle on={selected.has(ch.name)} />
          </Pressable>
        ))}

        {offer.privateFeeds.length > 0 ? (
          <>
            <Text style={[styles.section, { color: c.text }]}>Included with this order</Text>
            <Small muted>These are added automatically. They contain your order details only.</Small>
            <Card style={styles.gap}>
              {offer.privateFeeds.map((f) =>
                f.valid ? (
                  <View key={f.url}>
                    <Body>{f.displayName ?? 'Private feed'}</Body>
                    {f.purpose ? <Small muted>{f.purpose}</Small> : null}
                  </View>
                ) : (
                  <Small key={f.url} muted>
                    One link could not be verified, so it was not added.
                  </Small>
                ),
              )}
            </Card>
          </>
        ) : null}

        <Button
          title="Subscribe"
          busy={busy}
          disabled={selected.size === 0 && offer.privateFeeds.length === 0}
          onPress={async () => {
            setBusy(true);
            try {
              await onSubscribe([...selected]);
            } catch {
              setBusy(false);
            }
          }}
        />
        <Button title="Back" variant="secondary" disabled={busy} onPress={onBack} />
      </View>
    </Screen>
  );
}

/**
 * The first-company "Turn on notifications" screen: the only prompt surface,
 * with no skip. On iOS/Android the native permission dialog is the tap; on the
 * web the button tap is the user gesture the browser requires.
 *
 * The app-wide state gates the screen first: an install with no wake-up
 * transport gets the ntfy guidance instead of a prompt it cannot satisfy, and
 * the probe still running shows a neutral spinner.
 */
function NotificationsScreen({
  onEnable,
  onDone,
}: {
  onEnable: () => Promise<SelfTestResult>;
  onDone: () => void;
}) {
  const c = usePalette();
  const [phase, setPhase] = useState<'idle' | 'busy' | 'pending' | 'failed' | 'green'>('idle');
  const { notification, actions } = useApp();

  useEffect(() => {
    if (phase !== 'pending') return;
    if (notification.kind === 'ok' && notification.testedAt) setPhase('green');
    else if (notification.kind === 'failed') setPhase('failed');
  }, [phase, notification.kind, notification.testedAt]);

  useEffect(() => {
    if (phase !== 'green') return;
    const t = setTimeout(onDone, 2000);
    return () => clearTimeout(t);
  }, [phase, onDone]);

  if (notification.kind === 'checking') {
    return (
      <Screen>
        <View style={styles.pad}>
          <Title>Notifications</Title>
          <Spinner label="Checking notifications…" />
        </View>
      </Screen>
    );
  }

  if (notification.kind === 'no-transport') {
    return (
      <Screen>
        <View style={styles.pad}>
          <Title>Notifications need ntfy</Title>
          <Body muted>
            This phone has no Google services, so Keryx uses ntfy — a free, open-source push app — to
            deliver timely updates.
          </Body>
          <Small muted>Install ntfy, then let it run so wake-ups are not delayed.</Small>
          <Button title="Install ntfy" onPress={() => void openExternal(NTFY_INSTALL_URL)} />
          <Button
            title="Check again"
            variant="secondary"
            onPress={() => void actions.checkNotifications()}
          />
        </View>
      </Screen>
    );
  }

  return (
    <Screen>
      <View style={styles.pad}>
        <Title>Turn on notifications</Title>
        <Body muted>
          Timely updates — security incidents and order status — reach this device only with
          notifications on.
        </Body>
        <Small muted>
          Only the companies you follow can reach you, and only on the channels you keep on. Turn any
          channel off to silence it.
        </Small>
        {phase === 'busy' ? (
          <Spinner label="Setting up notifications…" />
        ) : phase === 'green' ? (
          <Alert>
            <Body>Notifications are working.</Body>
          </Alert>
        ) : phase === 'pending' ? (
          <Alert>
            <Body>
              Notifications are on. The first test is still on its way — it can take a minute; you can
              keep using the app.
            </Body>
          </Alert>
        ) : phase === 'failed' ? (
          <Alert danger>
            <Body>
              {notification.leg === 'topic'
                ? 'Notifications could not be set up. Try again.'
                : 'Notifications are off. Allow them in your system settings, then try again.'}
            </Body>
          </Alert>
        ) : null}
        <Button title="Turn on" busy={phase === 'busy'} onPress={async () => {
            setPhase('busy');
            const result = await onEnable();
            if (result.endpoint === 'delivered') onDone();
            else setPhase(result.endpoint === 'pending' ? 'pending' : 'failed');
          }}
        />
        {phase === 'pending' ? <Button title="Continue" variant="secondary" onPress={onDone} /> : null}
        <View style={styles.flexSpacer} />
      </View>
    </Screen>
  );
}

const styles = StyleSheet.create({
  pad: { flex: 1, padding: spacing(2.5), gap: spacing(1.5) },
  gap: { marginVertical: spacing(1) },
  identity: { flexDirection: 'row', alignItems: 'center', gap: spacing(1.5) },
  section: { fontSize: type.section, fontWeight: '600' },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing(1.5),
    paddingVertical: spacing(1.5),
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
  input: {
    minHeight: 50,
    borderRadius: radius.control,
    borderWidth: 1,
    paddingHorizontal: spacing(1.5),
    fontSize: type.body,
  },
  flexSpacer: { flex: 1 },
});
