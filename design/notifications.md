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
| Post-pair prompt | per company | the consent moment right after pairing |
| "Topics pending" marker | per company, transient | shown only until the union catches up |

The registration is keyed by relay base URL even though there is exactly one
entry today: if on-premise relays ever return (relay spec §11), they become
additional entries rather than a rewrite. Every change — pairing,
follow/unfollow, company removal, wake-up heartbeat — recomputes the union and
`PUT`s it; a `409`/`401` obtains a fresh subscription and re-registers.

## States

| State | Detection | Presentation |
|---|---|---|
| Unsupported | no `Notification`/`PushManager`/SW, or `!isSecureContext` | neutral note: wake-ups unavailable, polling continues |
| Not asked | `Notification.permission === 'default'` | red top bar + "Turn on" — a tap is the user gesture the prompt needs |
| Blocked | `Notification.permission === 'denied'` | red top bar + "Check again" + one help URL |
| Granted, no subscription | `getSubscription() === null` | red top bar + "Turn on" |
| Registered, relay says gone | heartbeat `404`/`401`, update `409` | red top bar + "Re-subscribe" |
| Registered, test failed | self-test per-leg result | red top bar + the failing leg |
| Healthy | permission granted, subscription present, registration current, test ok | subtle "Wake-ups on" row |

Placement: a red top bar on the company list, the post-pair "Turn on" on the
consent screen, and a transient per-company marker while that company's topics are
not yet in the union. The bar re-checks on `visibilitychange` and after every
sync, so it clears itself the moment the user unblocks notifications.

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

- browser wake-up: delivered / not delivered;
- native wake-up: sent / not sent (device receipt is unobservable).

The app generates a nonce, persists it, and sends it with the request; its own
handler accepts the test only when it sees that nonce, so a forged test can never
produce a false "delivered". A test is never a wake-up: it fetches no content,
advances no sequence, and consumes no recovery allowance or publish budget.
