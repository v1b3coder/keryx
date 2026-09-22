/**
 * Pairing flow: input (scan/paste) → confirm the origin (the only human
 * step — nothing is fetched before it, no name/logo shown) → consent summary
 * (company in the publisher's own signed words) → subscribe.
 */

import { useEffect, useRef, useState } from 'react';
import { Capacitor } from '@capacitor/core';
import { QrCode, ClipboardText, ArrowLeft, ArrowRight } from '@phosphor-icons/react';
import { parseJoinUrl, type JoinPayload } from '../lib/payload';
import { buildPairingOffer, createCompanyFromOffer, type PairingOffer } from '../lib/pair';
import { scanQr } from '../lib/scan';
import { syncCompany } from '../lib/sync';
import { getAllCompanies, getItems, deleteItems, type CompanyRecord } from '../lib/store';
import { relayBaseUrl } from '../lib/relay';
import { ensureRelayRegistration, topicBindings } from '../lib/relay-sw';
import { permissionState, type SelfTestResult } from '../lib/notify';
import { CompanyLogo } from './CompanyLogo';
import { useApp } from '../state';

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
  /** set when opened from a PWA deep link — pairing starts without the input screen */
  initialUrl?: string;
}) {
  const [step, setStep] = useState<Step>({ t: 'input' });
  const [pasting, setPasting] = useState(false);
  const [pasteValue, setPasteValue] = useState('');
  const videoRef = useRef<HTMLVideoElement>(null);
  const { actions, companies } = useApp();

  // PWA deep link (?domain=&p=): go straight to the origin confirmation.
  useEffect(() => {
    if (initialUrl) startPairing(initialUrl);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Web scan: run the detector against the visible preview element; abort on
  // cancel or unmount. (Native platforms use the MLKit scanner, no preview.)
  const scanning = step.t === 'scan';
  useEffect(() => {
    if (!scanning) return;
    const video = videoRef.current;
    if (!video) return;
    const ctrl = new AbortController();
    scanQr(video, ctrl.signal)
      .then((text) => {
        if (ctrl.signal.aborted) return;
        if (text) startPairing(text);
        else setStep({ t: 'input', error: 'No QR code found. Try again or paste the link.' });
      })
      .catch(() => {
        if (ctrl.signal.aborted) return;
        setStep({ t: 'input', error: 'Camera is not available. Paste the link instead.' });
      });
    return () => ctrl.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scanning]);

  function handleScan() {
    if (Capacitor.isNativePlatform()) {
      // Native: MLKit provides its own full-screen camera UI, so there is no
      // in-app preview step (and no empty <video> artefact before it).
      void scanQr()
        .then((text) => {
          if (text) startPairing(text);
          else setStep({ t: 'input', error: 'No QR code found. Try again or paste the link.' });
        })
        .catch(() => {
          setStep({ t: 'input', error: 'Camera is not available. Paste the link instead.' });
        });
      return;
    }
    setStep({ t: 'scan' });
  }

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
      setStep({ t: 'error', message: 'This QR code is for a different company. Use the QR from the company you already follow.' });
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
      const permission = permissionState();
      if (permission === 'granted' && (await registrationCurrent(company))) {
        await actions.runNotificationSelfTest();
        onDone(company.origin);
        return;
      }
    }
    setStep({ t: 'notifications', origin: company.origin });
  }

  if (step.t === 'scan') {
    return (
      <div className="screen screen-pad" style={{ paddingTop: 48 }}>
        <h1 className="t-title" style={{ margin: 0 }}>
          Scan QR code
        </h1>
        <div
          className="card"
          style={{ position: 'relative', overflow: 'hidden', padding: 0, margin: '16px 0' }}
        >
          <video
            ref={videoRef}
            autoPlay
            playsInline
            muted
            style={{ display: 'block', width: '100%', aspectRatio: '3/4', objectFit: 'cover' }}
          />
          <div
            style={{
              position: 'absolute',
              inset: 0,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              pointerEvents: 'none',
            }}
          >
            <div style={{ width: 200, height: 200, border: '2px solid #fff', borderRadius: 12 }} />
          </div>
        </div>
        <p className="t-body t-muted" style={{ margin: '0 0 16px' }}>
          Point the camera at the company's QR code.
        </p>
        <button className="btn btn-secondary" onClick={() => setStep({ t: 'input' })}>
          Cancel
        </button>
      </div>
    );
  }

  if (step.t === 'input') {
    return (
      <div className="screen screen-pad" style={{ paddingTop: 48 }}>
        <div className="empty" style={{ alignItems: 'stretch', textAlign: 'left' }}>
          <h1 className="t-title" style={{ margin: 0 }}>
            Add a company
          </h1>
          <p className="t-body t-muted" style={{ margin: '0 0 24px' }}>
            Scan the QR code a company printed or showed you. Its announcements will
            appear here, verified.
          </p>
          {pasting ? (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              <input
                className="input"
                autoFocus
                placeholder="Paste the company link or domain"
                value={pasteValue}
                onChange={(e) => setPasteValue(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && pasteValue.trim()) startPairing(pasteValue);
                }}
              />
              <button className="btn btn-primary" disabled={!pasteValue.trim()} onClick={() => startPairing(pasteValue)}>
                Continue
              </button>
              <button className="btn btn-secondary" onClick={() => { setPasting(false); setPasteValue(''); }}>
                Back
              </button>
            </div>
          ) : (
            <>
              <button className="btn btn-primary" onClick={() => void handleScan()}>
                <QrCode size={22} weight="bold" /> Scan QR code
              </button>
              <button className="btn btn-secondary" onClick={() => setPasting(true)}>
                <ClipboardText size={20} /> Paste a link
              </button>
              <button className="btn btn-ghost" onClick={onCancel}>
                <ArrowLeft size={20} /> Back
              </button>
            </>
          )}
          {step.error && (
            <div className="alert alert-danger" style={{ marginTop: 8 }}>
              <p>{step.error}</p>
            </div>
          )}
        </div>
      </div>
    );
  }

  if (step.t === 'confirm') {
    return (
      <div className="screen screen-pad" style={{ paddingTop: 48 }}>
        <h1 className="t-title" style={{ marginBottom: 8 }}>
          Confirm this website
        </h1>
        <p className="t-body t-muted" style={{ marginTop: 0 }}>
          You are subscribing to messages from:
        </p>
        <div className="card" style={{ margin: '16px 0', padding: 20 }}>
          <div className="t-mono" style={{ fontSize: 16, wordBreak: 'break-all' }}>
            {step.origin}
          </div>
        </div>
        <p className="t-small" style={{ marginTop: 0 }}>
          Check that this is the company's real website address. This is the only thing
          you confirm. Everything after this is verified automatically.
        </p>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginTop: 16 }}>
          <button className="btn btn-primary" onClick={() => void confirmOrigin()}>
            Continue <ArrowRight size={20} />
          </button>
          <button className="btn btn-secondary" onClick={onCancel}>
            Cancel
          </button>
        </div>
      </div>
    );
  }

  if (step.t === 'loading') {
    return (
      <div className="screen">
        <div className="empty">
          <div className="spinner" />
          <p className="t-body t-muted">Checking the company's signed metadata…</p>
        </div>
      </div>
    );
  }

  if (step.t === 'consent') {
    return <ConsentScreen offer={step.offer} onSubscribe={subscribe} onBack={() => setStep({ t: 'confirm', origin: step.offer.origin, joinUrl: step.offer.joinUrl, payload: parseJoinUrl(step.offer.joinUrl).payload })} />;
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
    <div className="screen screen-pad" style={{ paddingTop: 48 }}>
      <div className="alert alert-danger">
        <h3>Could not add this company</h3>
        <p>{step.message}</p>
      </div>
      <div style={{ marginTop: 16 }}>
        <button className="btn btn-primary" onClick={onCancel}>
          Back
        </button>
      </div>
    </div>
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
  const [selected, setSelected] = useState<Set<string>>(
    () => new Set(offer.channels.filter((c) => c.suggested).map((c) => c.name)),
  );
  // subscribe() runs the TUF chain, which can take seconds: show it in the button
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
    <div className="screen screen-pad" style={{ paddingTop: 32 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginBottom: 20 }}>
        <CompanyLogo url={offer.logo} origin={offer.origin} expectedSha={offer.logoSHA256} size={56} />
        <div>
          <div className="t-section">{offer.companyName}</div>
          <div className="t-mono" style={{ fontSize: 12, color: 'var(--text-2)' }}>
            {offer.origin}
          </div>
        </div>
      </div>

      <h2 className="t-section" style={{ marginBottom: 4 }}>
        Choose what to follow
      </h2>
      <p className="t-small" style={{ marginTop: 0 }}>
        Tap to subscribe to each channel. You can change this later.
      </p>

      <div style={{ margin: '8px 0' }}>
        {offer.channels.map((c) => (
          <button key={c.name} className="row" onClick={() => toggle(c.name)}>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div className="t-body" style={{ fontWeight: 600 }}>
                {c.displayName}
              </div>
              {c.description && (
                <div className="t-small" style={{ marginTop: 2 }}>
                  {c.description}
                </div>
              )}
            </div>
            <div className={`toggle ${selected.has(c.name) ? 'toggle-on' : ''}`} aria-label={c.displayName} />
          </button>
        ))}
      </div>

      {offer.privateFeeds.length > 0 && (
        <>
          <h2 className="t-section" style={{ margin: '16px 0 4px' }}>
            Included with this order
          </h2>
          <p className="t-small" style={{ marginTop: 0 }}>
            These are added automatically. They contain your order details only.
          </p>
          <div className="card" style={{ marginBottom: 8 }}>
            {offer.privateFeeds.map((f) =>
              f.valid ? (
                <div key={f.url} className="row" style={{ borderBottom: 'none' }}>
                  <div style={{ flex: 1 }}>
                    <div className="t-body" style={{ fontWeight: 600 }}>
                      {f.displayName ?? 'Private feed'}
                    </div>
                    {f.purpose && <div className="t-small">{f.purpose}</div>}
                  </div>
                </div>
              ) : (
                <div key={f.url} className="row" style={{ borderBottom: 'none' }}>
                  <div className="t-small t-muted">One link could not be verified, so it was not added.</div>
                </div>
              ),
            )}
          </div>
        </>
      )}

      <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginTop: 12, paddingBottom: 24 }}>
        <button
          className="btn btn-primary"
          style={{ position: 'relative' }}
          disabled={busy || (selected.size === 0 && offer.privateFeeds.length === 0)}
          aria-busy={busy}
          onClick={async () => {
            setBusy(true);
            try {
              await onSubscribe([...selected]);
            } catch {
              setBusy(false);
            }
          }}
        >
          {/* the label stays in place; the spinner is overlaid so nothing shifts */}
          <span style={{ opacity: busy ? 0 : 1 }}>Subscribe</span>
          {busy && <span className="spinner spinner-btn" aria-hidden="true" />}
        </button>
        <button className="btn btn-secondary" disabled={busy} onClick={onBack}>
          Back
        </button>
      </div>
    </div>
  );
}

/**
 * The first-company "Turn on notifications" screen (design/notifications.md):
 * the only prompt surface, with no skip. The tap is the user gesture the
 * browser requires; the app then registers and self-tests. A slow first push
 * is neutral, never red: the pending nonce keeps listening until it lands.
 */
function NotificationsScreen({
  onEnable,
  onDone,
}: {
  onEnable: () => Promise<SelfTestResult>;
  onDone: () => void;
}) {
  const [phase, setPhase] = useState<'idle' | 'busy' | 'pending' | 'failed' | 'green'>('idle');
  const { notification } = useApp();

  // the app-wide state upgrades when the late nonce lands: show green briefly,
  // then continue to the company view
  useEffect(() => {
    if (phase !== 'pending') return;
    if (notification.kind === 'ok' && notification.testedAt) setPhase('green');
    else if (notification.kind === 'failed') setPhase('failed');
  }, [phase, notification.kind, notification.testedAt]);

  // continue once, and never cancel the timer just because the app-wide state
  // was re-read (the notification object is rebuilt on every poll)
  useEffect(() => {
    if (phase !== 'green') return;
    const t = setTimeout(onDone, 2000);
    return () => clearTimeout(t);
  }, [phase, onDone]);

  return (
    <div className="screen screen-pad" style={{ paddingTop: 48 }}>
      <h1 className="t-title" style={{ margin: 0 }}>
        Turn on notifications
      </h1>
      <p className="t-body t-muted" style={{ margin: '8px 0 24px' }}>
        Timely updates — security incidents and order status — reach this device
        only with notifications on.
      </p>
      {phase === 'busy' ? (
        <div className="empty" style={{ padding: 0, alignItems: 'flex-start' }}>
          <div className="spinner" />
          <p className="t-small t-muted">Setting up wake-ups…</p>
        </div>
      ) : phase === 'green' ? (
        <div className="alert" style={{ marginBottom: 16 }}>
          <p>Notifications are working.</p>
        </div>
      ) : phase === 'pending' ? (
        <div className="alert" style={{ marginBottom: 16 }}>
          <p>
            Wake-ups are on. The first test is still on its way — it can take a
            minute; you can keep using the app.
          </p>
        </div>
      ) : phase === 'failed' ? (
        <div className="alert alert-danger" style={{ marginBottom: 16 }}>
          <p>
            Notifications are off. Allow them in your browser or system settings,
            then try again.
          </p>
        </div>
      ) : null}
      <button
        className="btn btn-primary"
        disabled={phase === 'busy'}
        onClick={async () => {
          setPhase('busy');
          const result = await onEnable();
          if (result.endpoint === 'delivered') onDone();
          else setPhase(result.endpoint === 'pending' ? 'pending' : 'failed');
        }}
      >
        Turn on
      </button>
      {phase === 'pending' && (
        <button className="btn btn-secondary" style={{ marginTop: 10 }} onClick={onDone}>
          Continue
        </button>
      )}
    </div>
  );
}

/** Whether this company's topics are already registered on the relay. */
async function registrationCurrent(company: CompanyRecord): Promise<boolean> {
  const base = relayBaseUrl();
  if (!base) return false;
  const relay = await ensureRelayRegistration(await getAllCompanies());
  if (!relay) return false;
  const topics = Object.keys(topicBindings(company));
  return topics.every((t) => t in relay.topics);
}
