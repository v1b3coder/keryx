# Notification UX and the self-test

How the app presents, enables, and tests relay wake-ups
([`../relay/SPECIFICATION.md`](../relay/SPECIFICATION.md) §4.2, §5.3.1).

The relay is centralized: one relay serves every company (relay spec §11), so
everything below the company layer is **per install**, not per company. A
company-scoped registration would collide on the second company (`409`) and
deleting one company would delete the other's registration.

## One install, one subscription, one registration

| Layer | Scope | Notes |
|---|---|---|
| `Notification.permission` | app-wide | the browser grants it per origin — one value for every company |
| `PushManager` subscription | app-wide | one browser endpoint per relay VAPID key |
| Relay registration | app-wide | one record holding the **union of topics** of every followed company |
| Followed topics | per company | derived from each company's channels (relay spec §3) |
| First-company prompt | first company only | the dedicated turn-on screen after channel selection (no skip) |
| "Topics pending" marker | per company, transient | shown only until the union catches up |

The registration is keyed by relay base URL even though there is exactly one
entry today: if on-premise relays ever return (relay spec §11), they become
additional entries rather than a rewrite. Every change — pairing,
follow/unfollow, company removal — recomputes the union and `PUT`s it; the
wake-up heartbeat and the foreground check extend `last_seen` and obtain a fresh
subscription and registration on `404`/`401`.

## Transport selection

One install has exactly one wake-up transport, chosen by a capability probe, never by
a user setting:

| Device | Transport | Probe |
|---|---|---|
| Web / PWA | browser `PushManager` — the endpoint leg (relay spec §6.2) | `PushManager` present |
| Android with Google services | FCM topics — the topic leg (relay spec §6.1) | `com.google.android.gms` present |
| De-Googled Android | UnifiedPush distributor (ntfy today) — the endpoint leg over the distributor's connection (relay spec §6.3) | a distributor answers `org.unifiedpush.android.distributor.REGISTER` |
| Android without either | none | neither probe |

The probe runs at startup and on `visibilitychange`, so installing ntfy and
returning to the app clears the state without a restart. FCM is chosen only when
the device can actually use it; if an FCM registration fails, the app falls back
to UnifiedPush before it gives up (the FCM phase). The relay needs **no change**
for ntfy: its endpoint leg already delivers RFC 8291/VAPID WebPush to any approved
push origin, and `ntfy.sh` is approved by default (relay spec §5.6). A self-hosted
ntfy server must be added to the relay's approved push origins.

When the transport is `none` on Android, the state is **"notifications need
ntfy"**: the red bar and the first-company screen carry the install link plus the
battery-optimization note. The web `unsupported` state (no `PushManager`) stays a
neutral note — there installing an app cannot fix it, and polling is the backstop.

**Mock (until the FCM phase):** the native probe reports no Google services
(`fcm: false`) unconditionally, so the Android app exercises the ntfy branch; the
distributor probe itself is real.

## States

| State | Detection | Presentation |
|---|---|---|
| Unsupported | no `Notification`/`PushManager`/SW, or `!isSecureContext` | neutral note: wake-ups unavailable, polling continues |
| Not asked | `Notification.permission === 'default'` | first-company screen, else red top bar + "Turn on" — the tap is the user gesture the prompt needs |
| Blocked | `Notification.permission === 'denied'` | red top bar + "Check again" + one help URL |
| Granted, no subscription | `getSubscription() === null` | red top bar + "Turn on" |
| Registered, relay says gone | heartbeat `404`/`401`, update `409` | red top bar + "Re-subscribe" |
| Registered, test failed | self-test per-leg result | red top bar + the failing leg |
| Android, no transport | no Google services and no UnifiedPush distributor | red top bar + "Install ntfy" (see Transport selection) |
| Android, ntfy ready | a distributor is present; registration is the next phase (mock) | neutral note until the UnifiedPush phase |
| Healthy | permission granted, subscription present, registration current | no bar — it is reserved for attention states and the enable flow's tail |

Placement: the **first-company "Turn on notifications" screen** (no skip),
then the top bar on the company list, plus a transient per-company marker while
that company's topics are not yet in the union. The bar is red when wake-ups need
attention and neutral while a test is in flight; a healthy install shows no bar. The
green "Notifications are working" is only the tail of an enable/retry in the same
session — never a persistent status row, never after a reload. The bar re-checks on
`visibilitychange` and after every sync, so it clears itself the moment the user
unblocks notifications.

## First company: the turn-on screen

After pairing and channel selection, the first company shows a dedicated "Turn on
notifications" screen before the company view. It is the only prompt surface: the
tap is the user gesture the browser requires, and timely updates are the point of
the app, so there is no skip.

- CTA tapped → the system/browser prompt appears.
  - granted → progress state, register, self-test (below), green, company view.
  - denied or dismissed → company view with the red top bar and "Check again"
    (the prompt will not reappear; the bar explains how to unblock).
- The prompt is shown only while `permission === 'default'`. A second company
  (or a reinstall) with permission already granted and the registration current
  skips the screen entirely and runs the self-test silently: no second prompt is
  possible and the app-wide state is already on.

## Prompt policy

The prompt may only be shown while `permission === 'default'`. After a denial no
browser shows it again, so "Check again" re-reads the state instead of
re-prompting, and the wording stays generic ("allow notifications in your browser
or system settings") with one help URL the project maintains — the unblock path
differs per browser and platform and the app does not keep a matrix of it.

`permission === 'granted'` does not prove the system will display a notice: the
OS-level toggle is invisible to the API. The self-test is the honest answer to
that, instead of a "no wake-up for N days" heuristic, which false-positives on
companies that publish rarely.

## Self-test

"Check notifications" sends one test delivery per leg (relay spec §5.3.1) and
reports each separately:

| Leg | Receiver | Result wording |
|---|---|---|
| Endpoint, PWA | the service worker | browser wake-up delivered / not delivered |
| Endpoint, de-Googled Android | the UnifiedPush connector → the app handler | browser wake-up delivered / not delivered |
| Topic, native Android/iOS | the FCM handler | native wake-up sent / not sent (device receipt is unobservable) |

Every platform uses one of the two legs, so the two tests cover all of them.
The receiver's rule is the same everywhere: accept a §4.3 payload only while
its `nonce` matches the pending test, then report that leg's result. The relay
generates the nonce (and, for the topic leg, the short-lived topic it publishes
to) and returns them with the test; the client cannot choose the topic, and a
forged test can never produce a false "delivered".

A test is never a wake-up: it fetches no content, advances no sequence, and
consumes no recovery allowance or publish budget. The topic leg's handshake needs
the native shell (FCM topic subscribe) — future work alongside the native shell;
the endpoint leg's test works today for both the PWA and the UnifiedPush
connector.

### Outcome rendering (no red flicker)

The state machine has an explicit `testing` state that is never red, so the happy
path never flashes a red bar:

| Phase | UI |
|---|---|
| Registering + testing | neutral progress ("Setting up wake-ups…") |
| Endpoint leg delivered | progress continues while the topic leg is pending |
| Both legs settled ok | **green** "Notifications are working" — auto-dismisses ~6 s into no bar; shown only as the tail of an enable/retry in the same session, never after a reload |
| Endpoint leg failed, or registration failed | red bar with the failing leg + "Try again" |
| Topic leg not confirmed within ~20 s | neutral "sent — not confirmed yet"; upgrades to green if the handler reports the nonce later |

Red appears only on a definitive failure — never during the in-flight window and
never for a slow push service. A late topic confirmation upgrades the neutral state
to green while the enable flow's session lasts; it never flashes red first. "Check
again" re-runs the same sequence: re-read permission, re-register if needed,
self-test, green/red.
