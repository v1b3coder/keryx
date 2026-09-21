# App-wide Notifications and the Endpoint Self-Test — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the PWA's wake-up registration app-wide (one record holding the union of every followed company's topics), add the relay's endpoint-leg self-test, and surface it through the first-company "Turn on notifications" screen and the notification status banner.

**Architecture:** The relay gains one new endpoint (`POST /v1/registrations/{id}/test`) that delivers the §4.3 self-test payload through the ordinary endpoint path. The app replaces the per-company `CompanyRecord.relay` field with one registration record keyed by relay base URL, computes the topic union on every change, and adds a state machine (`src/lib/notify.ts`) plus a banner. The FCM topic-leg self-test (relay-generated topic handshake) is **out of scope**: it needs the native shell, and gets its own plan.

**Tech Stack:** Go 1.26 (`net/http`, `modernc.org/sqlite`), TypeScript, React, `idb` (IndexedDB), Vitest, `vitest-websocket-mock`-free fetch stubs.

**Spec:** `relay/SPECIFICATION.md` §4.3, §5.3.1, §5.4; `design/notifications.md`; `app/README.md`.

---

## File Structure

| File | Responsibility |
|---|---|
| `relay/internal/wakeup/test.go` (new) | the §4.3 self-test payload and its nonce |
| `relay/internal/store/store.go` (modify) | `RegistrationForManagement` (auth + lookup) |
| `relay/internal/relay/dispatch.go` (modify) | `SendToEndpoint` — one endpoint send, outside the queue |
| `relay/internal/api/api.go` (modify) | `POST /v1/registrations/{id}/test` + its limiters |
| `app/src/lib/store.ts` (modify) | `registrations` store (app-wide); drop `CompanyRecord.relay`; test/push records |
| `app/src/lib/relay.ts` (modify) | `testRegistration` client |
| `app/src/lib/relay-sw.ts` (modify) | union registration, 409/401 recovery, test payload handling |
| `app/src/lib/notify.ts` (new) | the notification state machine and the self-test runner |
| `app/src/ui/NotificationBanner.tsx` (new) | red/neutral/green banner + app-wide card |
| `app/src/ui/Contacts.tsx` (modify) | mount the banner |
| `app/src/ui/Company.tsx` (modify) | drop the per-company notification chip |
| `app/src/ui/AddCompany.tsx` (modify) | the first-company "Turn on notifications" step |
| `app/src/state.tsx` (modify) | the new actions |
| `app/src/sw.ts` (modify) | handle the test payload before the wake-up parse |

---

### Task 1: The §4.3 self-test payload (relay)

**Files:**
- Create: `relay/internal/wakeup/test.go`
- Test: `relay/internal/wakeup/test_test.go`

- [ ] **Step 1: Write the failing test**

```go
package wakeup

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestNewTestPayload(t *testing.T) {
	nonce, payload := NewTestPayload()
	if raw, err := base64.RawURLEncoding.DecodeString(nonce); err != nil || len(raw) != 32 {
		t.Fatalf("nonce = %q (%v)", nonce, err)
	}
	var p TestPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if p.V != 1 || !p.Test || p.Nonce != nonce {
		t.Fatalf("payload = %s", payload)
	}
	if _, _, _, err := Parse(payload); err == nil {
		t.Fatal("the test payload parsed as a wake-up")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd relay && go test ./internal/wakeup/ -run TestNewTestPayload -v`
Expected: FAIL — `undefined: NewTestPayload`

- [ ] **Step 3: Write the implementation**

```go
package wakeup

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
)

// TestPayload is the §4.3 self-test payload. It is never a wake-up: no topic,
// no seq, no signature. The relay generates it and the client accepts it only
// while it is waiting for that nonce (relay/SPECIFICATION.md §4.3, §5.3.1).
type TestPayload struct {
	V     int    `json:"v"`
	Test  bool   `json:"test"`
	Nonce string `json:"nonce"`
}

// NewTestPayload returns a fresh nonce (32 random bytes, base64url) and the
// canonical §4.3 payload that carries it.
func NewTestPayload() (nonce string, payload []byte) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	nonce = base64.RawURLEncoding.EncodeToString(raw)
	payload, _ = json.Marshal(TestPayload{V: 1, Test: true, Nonce: nonce})
	return nonce, payload
}
```

`crypto/rand.Read` and `json.Marshal` of a fixed struct cannot fail in practice; a panic is the honest signal if they do (the relay cannot mint a nonce safely).

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd relay && go test ./internal/wakeup/ -run TestNewTestPayload -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add relay/internal/wakeup/test.go relay/internal/wakeup/test_test.go
git commit -m "feat(relay): add the self-test payload"
```

---

### Task 2: Registration lookup with the management token (relay)

**Files:**
- Modify: `relay/internal/store/store.go` (after `HeartbeatRegistration`, around line 660)
- Test: `relay/internal/store/store_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestRegistrationForManagement(t *testing.T) {
	st, err := Open(":memory:", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, token, err := st.CreateRegistration("https://push.example/1", testP256DH, testAuth, "pwa", "", []string{topicA}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	reg, err := st.RegistrationForManagement(id, token)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if reg.Endpoint != "https://push.example/1" {
		t.Fatalf("endpoint = %q", reg.Endpoint)
	}
	if _, err := st.RegistrationForManagement(id, "wrong"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong token = %v", err)
	}
	if _, err := st.RegistrationForManagement("missing", token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id = %v", err)
	}
}
```

Use the existing `testP256DH`/`testAuth`/`topicA` helpers from `store_test.go`; if they are named differently, reuse the file's existing fixtures.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd relay && go test ./internal/store/ -run TestRegistrationForManagement -v`
Expected: FAIL — `st.RegistrationForManagement undefined`

- [ ] **Step 3: Write the implementation**

```go
// RegistrationForManagement returns one registration after checking its
// management token (relay/SPECIFICATION.md §5.3.1). It is the only read of a
// single registration; the registry stays unreadable without the token.
func (s *Store) RegistrationForManagement(id, managementToken string) (Registration, error) {
	var r Registration
	var created, seen string
	err := s.registry.QueryRow(`SELECT id, endpoint, p256dh, auth, source, created_at, last_seen, COALESCE(user_agent, '')
		FROM registrations WHERE id = ?`, id).
		Scan(&r.ID, &r.Endpoint, &r.P256DH, &r.Auth, &r.Source, &created, &seen, &r.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return Registration{}, ErrNotFound
	}
	if err != nil {
		return Registration{}, err
	}
	if r.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
		return Registration{}, err
	}
	if r.LastSeen, err = time.Parse(time.RFC3339, seen); err != nil {
		return Registration{}, err
	}
	var stored string
	if err := s.registry.QueryRow(`SELECT management_token_hash FROM registrations WHERE id = ?`, id).Scan(&stored); err != nil {
		return Registration{}, err
	}
	if stored != hashToken(managementToken) {
		return Registration{}, ErrUnauthorized
	}
	return r, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd relay && go test ./internal/store/ -run TestRegistrationForManagement -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add relay/internal/store/store.go relay/internal/store/store_test.go
git commit -m "feat(relay): add authenticated single-registration lookup"
```

---

### Task 3: The endpoint self-test endpoint (relay)

**Files:**
- Modify: `relay/internal/relay/dispatch.go` (add `SendToEndpoint` near `dispatch`)
- Modify: `relay/internal/api/api.go` (route, handler, limiters, options)
- Modify: `relay/cmd/relay/main.go` (new flags)
- Test: `relay/internal/api/api_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestRegistrationSelfTest(t *testing.T) {
	st, err := store.Open(":memory:", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	// a fake push service: it records the RFC 8291 body and returns 201
	var mu sync.Mutex
	var body []byte
	pushSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = b
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer pushSrv.Close()

	policy := netpolicy.New()
	policy.AllowPrivate = true
	policy.AllowHTTP = true
	vapid, _ := ecdh.P256().GenerateKey(rand.Reader)
	wp, _, err := push.NewWebPush(base64.RawURLEncoding.EncodeToString(vapid.Bytes()),
		"mailto:ops@example.com", time.Hour, policy.HTTPClient(false))
	if err != nil {
		t.Fatal(err)
	}
	d := relay.New(st, nil, wp, relay.Options{Logger: discardLogger()})
	srv := New(st, d, companytuf.New(st, nil, companytuf.Options{Logger: discardLogger()}), Options{
		Policy:              policy,
		ApprovedPushOrigins: []string{pushSrv.URL},
		Logger:              discardLogger(),
	})
	h := srv.Handler()

	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	regBody, _ := json.Marshal(map[string]any{
		"endpoint": pushSrv.URL + "/push",
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(ua.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(auth),
		},
		"topics": []string{validTopic()},
	})
	rec := do(t, h, http.MethodPost, "/v1/registrations", string(regBody), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	var created struct {
		ID    string `json:"id"`
		Token string `json:"management_token"`
	}
	json.Unmarshal(rec.Body.Bytes(), &created)

	rec = do(t, h, http.MethodPost, "/v1/registrations/"+created.ID+"/test", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d", rec.Code)
	}
	authHeader := map[string]string{"Authorization": "Bearer " + created.Token}
	rec = do(t, h, http.MethodPost, "/v1/registrations/"+created.ID+"/test", "", authHeader)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("test = %d: %s", rec.Code, rec.Body)
	}
	var result struct {
		Nonce     string `json:"nonce"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Nonce) != 43 || result.ExpiresAt == "" {
		t.Fatalf("result = %s", rec.Body)
	}
	mu.Lock()
	got := body
	mu.Unlock()
	if len(got) < 86 {
		t.Fatalf("push service received %d bytes", len(got))
	}
	// the payload is the §4.3 JSON, not a wake-up: decrypt it as in the e2e test
	plain := decryptRFC8291(t, ua, auth, got)
	var test_payload struct {
		V     int    `json:"v"`
		Test  bool   `json:"test"`
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(plain, &test_payload); err != nil {
		t.Fatal(err)
	}
	if test_payload.V != 1 || !test_payload.Test || test_payload.Nonce != result.Nonce {
		t.Fatalf("payload = %s", plain)
	}

	rec = do(t, h, http.MethodPost, "/v1/registrations/missing/test", "", authHeader)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing = %d", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/v1/registrations/"+created.ID+"/test", "", map[string]string{"Authorization": "Bearer wrong"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d", rec.Code)
	}
}
```

Copy `decryptRFC8291` from `relay/internal/e2e/e2e_test.go` into `api_test.go` (or move it into a small shared `internal/push/testutil` package — do not duplicate it silently).

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd relay && go test ./internal/api/ -run TestRegistrationSelfTest -v`
Expected: FAIL — `404` (no route)

- [ ] **Step 3: Add `SendToEndpoint` to the dispatcher**

In `relay/internal/relay/dispatch.go`:

```go
// ErrEndpointLegDisabled reports that no endpoint leg is configured.
var ErrEndpointLegDisabled = errors.New("endpoint leg disabled")

// SendToEndpoint delivers one payload to a single registration's endpoint
// (the §5.3.1 self-test). It is outside the dispatch queue and the replay
// cache: a test is never a wake-up.
func (d *Dispatcher) SendToEndpoint(ctx context.Context, r store.Registration, payload []byte) error {
	if d.webpush == nil {
		return ErrEndpointLegDisabled
	}
	return d.webpush.Send(ctx, r.Endpoint, r.P256DH, r.Auth, payload)
}
```

- [ ] **Step 4: Add the handler to the API server**

In `relay/internal/api/api.go`, register the route in `Handler()`:

```go
	mux.HandleFunc("POST /v1/registrations/{id}/test", s.handleRegistrationTest)
```

Add the limiter fields to `Server` (next to `regLimiter`) and initialise them in `New`:

```go
	testIP  *ratelimit.Limiter
	testReg map[string]*ratelimit.Limiter
	testMu  sync.Mutex
```

```go
		testIP:  ratelimit.New(opts.TestIPPerMin, opts.TestIPBurst),
		testReg: map[string]*ratelimit.Limiter{},
```

Add the options (defaults: 10/min per IP, burst 20; 3/min per registration, burst 5):

```go
	TestIPPerMin  int // default 10
	TestIPBurst   int // default 20
	TestPerMin    int // per registration, default 3
	TestBurst     int // default 5
	TestTTL       time.Duration // capability lifetime, default 5m
```

Add the handler (model it on `handleRegistrationHeartbeat`):

```go
func (s *Server) handleRegistrationTest(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing management token")
		return
	}
	if !s.testIP.Allow(s.remoteIP(r)) {
		writeError(w, http.StatusTooManyRequests, "self-test rate limit exceeded")
		return
	}
	id := r.PathValue("id")
	if !s.testLimiter(id).Allow(id) {
		writeError(w, http.StatusTooManyRequests, "self-test rate limit exceeded")
		return
	}
	reg, err := s.store.RegistrationForManagement(id, token)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "registration not found")
		return
	case errors.Is(err, store.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "invalid management token")
		return
	case err != nil:
		s.internalError(w, err)
		return
	}
	nonce, payload := wakeup.NewTestPayload()
	if err := s.dispatcher.SendToEndpoint(r.Context(), reg, payload); err != nil {
		switch {
		case errors.Is(err, relay.ErrEndpointLegDisabled):
			writeError(w, http.StatusServiceUnavailable, "endpoint leg disabled")
		case errors.Is(err, push.ErrGone):
			writeError(w, http.StatusGone, "endpoint reported dead")
		default:
			s.logger.Warn("self-test delivery failed", "err", err) // never the endpoint
			writeError(w, http.StatusServiceUnavailable, "provider unavailable")
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"nonce":      nonce,
		"expires_at": time.Now().UTC().Add(s.opts.TestTTL).Format(time.RFC3339),
	})
}

// testLimiter returns the per-registration self-test bucket.
func (s *Server) testLimiter(id string) *ratelimit.Limiter {
	s.testMu.Lock()
	defer s.testMu.Unlock()
	l, ok := s.testReg[id]
	if !ok {
		l = ratelimit.New(s.opts.TestPerMin, s.opts.TestBurst)
		s.testReg[id] = l
	}
	return l
}
```

In `New`, default `TestTTL` to 5 minutes and the limits when unset (mirror the existing `if opts.X <= 0` block). In `Cleanup`, call `s.testIP.Cleanup(idle)` and clean `s.testReg` like `pubLimiters`.

- [ ] **Step 5: Add the flags**

In `relay/cmd/relay/main.go` `Config`:

```go
	TestIPPerMin int
	TestIPBurst  int
	TestPerMin   int
	TestBurst    int
	TestTTL      time.Duration
```

Load them from `RELAY_TEST_IP_PER_MIN` (10), `RELAY_TEST_IP_BURST` (20), `RELAY_TEST_PER_MIN` (3), `RELAY_TEST_BURST` (5), `RELAY_TEST_TTL_SECONDS` (300), register the flags next to `reg-per-min`, and pass them to `api.Options`. Update `relay/README.md`'s env list with the five names.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd relay && go test ./internal/api/ ./internal/relay/ -run 'TestRegistrationSelfTest|TestDispatcherIdle' -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add relay/internal/relay/dispatch.go relay/internal/api/api.go relay/internal/api/api_test.go relay/cmd/relay/main.go relay/README.md
git commit -m "feat(relay): add the endpoint self-test endpoint"
```

---

### Task 4: The app-wide registration store

**Files:**
- Modify: `app/src/lib/store.ts` (DB version, `registrations` store, drop `CompanyRecord.relay`, test/push records)
- Test: `app/src/lib/relay.test.ts` (store assertions live in `relay-sw.test.ts`; keep them there)

- [ ] **Step 1: Write the failing test**

In `app/src/lib/relay-sw.test.ts`:

```ts
import { getRegistration, putRegistration, deleteRegistrationRecord } from './store';

describe('app-wide registration store', () => {
  it('keeps one registration per relay base URL', async () => {
    await putRegistration({
      baseUrl: 'https://relay.example',
      id: 'reg-1',
      managementToken: 'tok-1',
      topics: {},
    });
    const got = await getRegistration('https://relay.example');
    expect(got?.id).toBe('reg-1');
    await deleteRegistrationRecord('https://relay.example');
    expect(await getRegistration('https://relay.example')).toBeUndefined();
  });
});
```

The file already runs under `fake-indexeddb` (see its header); reuse its setup.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd app && npx vitest run src/lib/relay-sw.test.ts -t 'app-wide registration store'`
Expected: FAIL — `putRegistration is not a function`

- [ ] **Step 3: Write the implementation**

In `app/src/lib/store.ts`, bump the version and create the store:

```ts
const DB_VERSION = 5;
```

```ts
        if (oldVersion > 0) {
          for (const name of ['contacts', 'items', 'media', 'companies', 'relay', 'registrations']) {
            if (db.objectStoreNames.contains(name)) db.deleteObjectStore(name);
          }
        }
```

```ts
        // the app-wide relay registration: one record per relay base URL,
        // holding the union of every followed company's topics
        db.createObjectStore('registrations', { keyPath: 'baseUrl' });
```

Add the accessors next to the company accessors:

```ts
export async function getRegistration(baseUrl: string): Promise<RelayRegistration | undefined> {
  const db = await openAppDb();
  return (await db.get('registrations', baseUrl)) as RelayRegistration | undefined;
}

export async function getRegistrations(): Promise<RelayRegistration[]> {
  const db = await openAppDb();
  return (await db.getAll('registrations')) as RelayRegistration[];
}

export async function putRegistration(reg: RelayRegistration): Promise<void> {
  const db = await openAppDb();
  await db.put('registrations', reg);
}

export async function deleteRegistrationRecord(baseUrl: string): Promise<void> {
  const db = await openAppDb();
  await db.delete('registrations', baseUrl);
}
```

Add the push/test records to the existing `relay` store (`keyPath: 'key'`):

```ts
/** The last wake-up this install accepted for a company (epoch ms). */
export async function markPushReceived(origin: string, at: number): Promise<void> {
  const db = await openAppDb();
  await db.put('relay', { key: `push\u0000${origin}`, at });
}

export async function lastPushAt(origin: string): Promise<number> {
  const db = await openAppDb();
  const rec = (await db.get('relay', `push\u0000${origin}`)) as RelayStateRecord | undefined;
  return rec?.at ?? 0;
}

/** The pending self-test the service worker matches by nonce (§5.3.1). */
export interface PendingTest {
  baseUrl: string;
  nonce: string;
  expiresAt: number;
  receivedAt?: number;
}

export async function putPendingTest(t: PendingTest): Promise<void> {
  const db = await openAppDb();
  await db.put('relay', { key: `test\u0000${t.baseUrl}`, ...t });
}

export async function pendingTest(baseUrl: string): Promise<PendingTest | undefined> {
  const db = await openAppDb();
  const rec = (await db.get('relay', `test\u0000${baseUrl}`)) as (PendingTest & { key: string }) | undefined;
  if (!rec) return undefined;
  return { baseUrl: rec.baseUrl, nonce: rec.nonce, expiresAt: rec.expiresAt, receivedAt: rec.receivedAt };
}

export async function clearPendingTest(baseUrl: string): Promise<void> {
  const db = await openAppDb();
  await db.delete('relay', `test\u0000${baseUrl}`);
}
```

Remove `relay?: import('./relay').RelayRegistration;` (and its comment) from `CompanyRecord`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd app && npx vitest run src/lib/relay-sw.test.ts src/lib/protocol.test.ts`
Expected: PASS (existing tests compile without `company.relay`; fix any test that sets it by dropping that field)

- [ ] **Step 5: Commit**

```bash
git add app/src/lib/store.ts app/src/lib/relay-sw.test.ts
git commit -m "feat(app): store the relay registration app-wide"
```

---

### Task 5: The union registration client and the self-test client

**Files:**
- Modify: `app/src/lib/relay.ts` (add `testRegistration`)
- Modify: `app/src/lib/relay-sw.ts` (`ensureRelayRegistration(companies)`, recovery)
- Test: `app/src/lib/relay.test.ts`, `app/src/lib/relay-sw.test.ts`

- [ ] **Step 1: Write the failing tests**

In `app/src/lib/relay.test.ts`:

```ts
it('posts the self-test and returns the nonce', async () => {
  vi.stubEnv('VITE_RELAY_URL', 'https://relay.example');
  vi.stubEnv('VITE_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
  let captured: { url: string; init: RequestInit } | null = null;
  vi.stubGlobal('fetch', (url: string, init: RequestInit) => {
    captured = { url, init };
    return Promise.resolve(new Response(JSON.stringify({ nonce: 'n-1', expires_at: '2026-01-01T00:00:00Z' }), { status: 202 }));
  });
  const got = await testRegistration('https://relay.example', 'reg-1', 'tok-1');
  expect(got.nonce).toBe('n-1');
  expect(captured?.url).toBe('https://relay.example/v1/registrations/reg-1/test');
  expect((captured?.init.headers as Record<string, string>).Authorization).toBe('Bearer tok-1');
  vi.unstubAllEnvs();
});
```

In `app/src/lib/relay-sw.test.ts`:

```ts
describe('app-wide registration', () => {
  it('registers the union of every company topic on one relay', async () => {
    const a = company('https://a.example', 'security');
    const b = company('https://b.example', 'news');
    const requests: string[] = [];
    vi.stubEnv('VITE_RELAY_URL', 'https://relay.example');
    vi.stubEnv('VITE_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
    vi.stubGlobal('fetch', (url: string, init: RequestInit) => {
      requests.push(`${init.method} ${url}`);
      return Promise.resolve(new Response(JSON.stringify({ id: 'reg-1', management_token: 'tok-1' }), { status: 200 }));
    });
    await ensureRelayRegistration([a, b]);
    const reg = await getRegistration('https://relay.example');
    expect(Object.keys(reg?.topics ?? {})).toHaveLength(2);
    expect(requests.some((r) => r === 'POST https://relay.example/v1/registrations')).toBe(true);
    vi.unstubAllEnvs();
  });
});
```

Define the `company()` helper in the test file (it builds a `CompanyRecord` with one followed channel); reuse the existing `topicBindings` fixtures if present.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd app && npx vitest run src/lib/relay.test.ts src/lib/relay-sw.test.ts`
Expected: FAIL — `testRegistration is not a function`, `ensureRelayRegistration` arity mismatch

- [ ] **Step 3: Write the implementation**

In `app/src/lib/relay.ts`, next to `relayHeartbeat`:

```ts
/** POST /v1/registrations/{id}/test — one endpoint self-test (§5.3.1). */
export async function testRegistration(
  baseUrl: string,
  id: string,
  managementToken: string,
): Promise<{ nonce: string; expiresAt: string }> {
  const res = await relayFetch(baseUrl, `/v1/registrations/${id}/test`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${managementToken}` },
  });
  if (res.status === 410) throw new Error('relay self-test: endpoint dead');
  if (!res.ok) throw new Error(`relay self-test: HTTP ${res.status}`);
  const body = (await res.json()) as { nonce?: string; expires_at?: string };
  if (!body.nonce || !body.expires_at) throw new Error('relay self-test: malformed response');
  return { nonce: body.nonce, expiresAt: body.expires_at };
}
```

In `app/src/lib/relay-sw.ts`, replace `ensureRelayRegistration(company)` with the union version:

```ts
/** The union of every followed company's topic bindings. */
export function unionTopics(companies: CompanyRecord[]): Record<string, TopicBinding> {
  const topics: Record<string, TopicBinding> = {};
  for (const company of companies) Object.assign(topics, topicBindings(company));
  return topics;
}

/**
 * Ensure the installation's registration matches every followed company's topics
 * (relay/SPECIFICATION.md §5.3). One record per relay holds the union; a `409`
 * or `401` means the local token is stale, so the app obtains a fresh browser
 * subscription and registers it. Returns the registration, or undefined when the
 * relay is unconfigured or the attempt failed (best-effort: independent content
 * sync remains available).
 */
export async function ensureRelayRegistration(
  companies: CompanyRecord[],
): Promise<RelayRegistration | undefined> {
  const base = relayBaseUrl();
  const vapid = vapidPublicKey();
  if (!base || !vapid) return undefined;
  const topics = unionTopics(companies);
  const topicList = Object.keys(topics);
  if (topicList.length === 0) {
    const existing = await getRegistration(base);
    if (existing) await deleteRegistration(base, existing.id, existing.managementToken);
    await deleteRegistrationRecord(base);
    return undefined;
  }
  let relay = await getRegistration(base);
  try {
    if (!relay) {
      const sub = await subscribePush(vapid);
      const created = await createRegistration(base, sub, topicList);
      relay = { baseUrl: base, id: created.id, managementToken: created.managementToken, topics };
    } else {
      await updateRegistration(base, relay.id, relay.managementToken, topicList);
      relay = { ...relay, topics };
    }
  } catch {
    // stale token or an existing endpoint: obtain a fresh subscription (§5.3)
    try {
      const previous = await pushManagerSubscription();
      if (previous) await previous.unsubscribe();
      const sub = await subscribePush(vapid);
      const created = await createRegistration(base, sub, topicList);
      relay = { baseUrl: base, id: created.id, managementToken: created.managementToken, topics };
    } catch {
      return undefined;
    }
  }
  await putRegistration(relay);
  return relay;
}

/** The browser's current push subscription, or null. */
async function pushManagerSubscription(): Promise<PushSubscription | null> {
  if (!('serviceWorker' in navigator)) return null;
  const registration = await navigator.serviceWorker.ready;
  return registration.pushManager.getSubscription();
}
```

Also in `relay-sw.ts`, add the test result handling used by the service worker:

```ts
/**
 * Handle a §4.3 self-test payload: accept it only while it matches the
 * pending test for its relay, then record receipt. Never a wake-up.
 */
export async function handleTestPayload(
  payload: unknown,
): Promise<{ accepted: boolean; origin?: string }> {
  const p = payload as { v?: number; test?: boolean; nonce?: string };
  if (p?.v !== 1 || p.test !== true || typeof p.nonce !== 'string') return { accepted: false };
  const registrations = await getRegistrations();
  for (const reg of registrations) {
    const pending = await pendingTest(reg.baseUrl);
    if (!pending || pending.nonce !== p.nonce) continue;
    if (Date.now() > pending.expiresAt) {
      await clearPendingTest(reg.baseUrl);
      return { accepted: false };
    }
    await putPendingTest({ ...pending, receivedAt: Date.now() });
    return { accepted: true };
  }
  return { accepted: false };
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd app && npx vitest run src/lib/relay.test.ts src/lib/relay-sw.test.ts`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add app/src/lib/relay.ts app/src/lib/relay-sw.ts app/src/lib/relay.test.ts app/src/lib/relay-sw.test.ts
git commit -m "feat(app): register the union of topics and add the self-test client"
```

---

### Task 6: The notification state machine and the self-test runner

**Files:**
- Create: `app/src/lib/notify.ts`
- Test: `app/src/lib/notify.test.ts`

- [ ] **Step 1: Write the failing test**

```ts
import { describe, it, expect, vi } from 'vitest';
import 'fake-indexeddb/auto';
import { notificationState, runSelfTest } from './notify';

function stubPermission(permission: NotificationPermission) {
  vi.stubGlobal('Notification', { permission, requestPermission: () => Promise.resolve(permission) });
}

describe('notification state machine', () => {
  it('reports unsupported when the browser has no push manager', async () => {
    vi.stubGlobal('Notification', undefined);
    expect((await notificationState()).kind).toBe('unsupported');
    vi.unstubAllGlobals();
  });

  it('reports the permission states', async () => {
    stubPermission('default');
    expect((await notificationState()).kind).toBe('default');
    stubPermission('denied');
    expect((await notificationState()).kind).toBe('denied');
    vi.unstubAllGlobals();
  });

  it('reports no-subscription when permission is granted and nothing is registered', async () => {
    stubPermission('granted');
    vi.stubEnv('VITE_RELAY_URL', 'https://relay.example');
    vi.stubEnv('VITE_VAPID_PUBLIC', 'BP8R9RtW5iPVjjmii5jkxGWAs7Q0XJ85DcFnV-tjjcEV_KGPWDC4LyU5ZQPP2XaGYoCOxAdfs4WqDa9HAF0h8gs');
    expect((await notificationState()).kind).toBe('no-subscription');
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd app && npx vitest run src/lib/notify.test.ts`
Expected: FAIL — `Cannot find module './notify'`

- [ ] **Step 3: Write the implementation**

```ts
/**
 * The app-wide notification state machine and self-test
 * (relay/SPECIFICATION.md §4.3, §5.3.1; design/notifications.md).
 *
 * The relay is centralized, so everything here is per install: one browser
 * permission, one push subscription, one relay record holding the union of every
 * followed company's topics. Nothing durable is per company.
 */

import {
  clearPendingTest,
  getAllCompanies,
  getRegistration,
  lastPushAt,
  pendingTest,
  putPendingTest,
  type CompanyRecord,
} from './store';
import {
  relayBaseUrl,
  testRegistration,
  vapidPublicKey,
} from './relay';
import {
  ensureRelayRegistration,
  topicBindings,
  unionTopics,
} from './relay-sw';
import type { RelayRegistration } from './relay';

export type NotificationStateKind =
  | 'unsupported'
  | 'default'
  | 'denied'
  | 'no-subscription'
  | 'unregistered'
  | 'failed'
  | 'ok';

export interface NotificationState {
  kind: NotificationStateKind;
  /** the failing leg when kind === 'failed' */
  leg?: 'endpoint' | 'registration';
  /** the last successful self-test (epoch ms) */
  testedAt?: number;
}

/** The browser's permission state, or 'unsupported'. */
export function permissionState(): NotificationPermission | 'unsupported' {
  if (typeof Notification === 'undefined' || typeof navigator === 'undefined') return 'unsupported';
  if (!('serviceWorker' in navigator) || !('PushManager' in window)) return 'unsupported';
  if (!window.isSecureContext) return 'unsupported';
  return Notification.permission;
}

/** The current app-wide notification state (no network, no prompt). */
export async function notificationState(): Promise<NotificationState> {
  const permission = permissionState();
  if (permission === 'unsupported') return { kind: 'unsupported' };
  if (permission === 'default') return { kind: 'default' };
  if (permission === 'denied') return { kind: 'denied' };
  const base = relayBaseUrl();
  const vapid = vapidPublicKey();
  if (!base || !vapid) return { kind: 'unsupported' };
  const registration = await getRegistration(base);
  if (!registration) return { kind: 'no-subscription' };
  const topics = Object.keys(unionTopics(await getAllCompanies()));
  if (!topics.every((t) => t in registration.topics)) {
    return { kind: 'unregistered' };
  }
  const pending = await pendingTest(base);
  if (pending?.receivedAt) return { kind: 'ok', testedAt: pending.receivedAt };
  const last = await lastPushAtForTopics(registration);
  return last ? { kind: 'ok', testedAt: last } : { kind: 'ok' };
}

async function lastPushAtForTopics(registration: RelayRegistration): Promise<number> {
  let last = 0;
  for (const origin of Object.keys(registration.topics)) {
    const at = await lastPushAt(originOfTopic(registration, origin));
    if (at > last) last = at;
  }
  return last;
}

export interface SelfTestResult {
  endpoint: 'delivered' | 'failed';
  /** the last successful self-test (epoch ms) */
  testedAt?: number;
  /** the failing leg when endpoint === 'failed' */
  leg: 'endpoint' | 'registration';
}

/**
 * Run the relay self-test (§5.3.1): ensure the registration, ask the relay
 * for a test, then wait up to ~10 s for the service worker to record the
 * matching nonce. Never a wake-up.
 */
export async function runSelfTest(companies: CompanyRecord[]): Promise<SelfTestResult> {
  const base = relayBaseUrl();
  if (!base) return { endpoint: 'failed', leg: 'registration' };
  const relay = await ensureRelayRegistration(companies);
  if (!relay) return { endpoint: 'failed', leg: 'registration' };
  try {
    const { nonce, expiresAt } = await testRegistration(base, relay.id, relay.managementToken);
    const expires = Date.parse(expiresAt);
    await putPendingTest({ baseUrl: base, nonce, expiresAt: expires });
    const deadline = Date.now() + 10_000;
    while (Date.now() < deadline) {
      const pending = await pendingTest(base);
      if (pending?.receivedAt) {
        return { endpoint: 'delivered', testedAt: pending.receivedAt, leg: 'endpoint' };
      }
      await sleep(500);
    }
    await clearPendingTest(base);
    return { endpoint: 'failed', leg: 'endpoint' };
  } catch {
    await clearPendingTest(base);
    return { endpoint: 'failed', leg: 'endpoint' };
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
```

`originOfTopic` does not exist: the registration's topic bindings already carry `{channel, scopeId}`, and `markPushReceived` is keyed by origin. Use the company record's origin instead: iterate `getAllCompanies()` and match each company's `topicBindings(company)` keys against the registration's topics. Replace `lastPushAtForTopics` with:

```ts
async function lastPushAtForTopics(registration: RelayRegistration): Promise<number> {
  let last = 0;
  for (const company of await getAllCompanies()) {
    for (const topic of Object.keys(topicBindings(company))) {
      if (!(topic in registration.topics)) continue;
      const at = await lastPushAt(company.origin);
      if (at > last) last = at;
    }
  }
  return last;
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd app && npx vitest run src/lib/notify.test.ts`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add app/src/lib/notify.ts app/src/lib/notify.test.ts
git commit -m "feat(app): add the notification state machine and self-test runner"
```

---

### Task 7: The service worker handles the test payload

**Files:**
- Modify: `app/src/lib/relay-sw.ts` (`handlePush` returns a test outcome)
- Modify: `app/src/sw.ts` (dispatch on `outcome.test`)
- Test: `app/src/lib/relay-sw.test.ts`

- [ ] **Step 1: Write the failing test**

```ts
it('records a test payload only when the pending nonce matches', async () => {
  await putPendingTest({ baseUrl: 'https://relay.example', nonce: 'n-1', expiresAt: Date.now() + 60_000 });
  expect(await handlePush(JSON.stringify({ v: 1, test: true, nonce: 'n-1' }))).toEqual({ accepted: true, test: true });
  const pending = await pendingTest('https://relay.example');
  expect(pending?.receivedAt).toBeGreaterThan(0);

  await putPendingTest({ baseUrl: 'https://relay.example', nonce: 'n-2', expiresAt: Date.now() + 60_000 });
  expect(await handlePush(JSON.stringify({ v: 1, test: true, nonce: 'wrong' }))).toEqual({ accepted: false });
  expect(await handlePush(JSON.stringify({ v: 1, test: true }))).toEqual({ accepted: false });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd app && npx vitest run src/lib/relay-sw.test.ts -t 'test payload'`
Expected: FAIL — the test payload is parsed as a wake-up and dropped

- [ ] **Step 3: Write the implementation**

In `app/src/lib/relay-sw.ts`, extend `PushOutcome`:

```ts
export interface PushOutcome {
  accepted: boolean;
  /** the payload was a §4.3 self-test, not a wake-up */
  test?: boolean;
  recovered?: boolean;
  title?: string;
  body?: string;
  origin?: string;
}
```

At the top of `handlePush`, before the wake-up parse:

```ts
  const maybeTest = testPayloadOf(data);
  if (maybeTest) {
    const result = await handleTestPayload(maybeTest);
    return { accepted: result.accepted, test: result.accepted };
  }
```

Add the parser (strict, like `parseWakeup`):

```ts
/** Parse a §4.3 self-test payload, or null when it is not one. */
function testPayloadOf(data: string | ArrayBuffer | Uint8Array): { v: number; test: true; nonce: string } | null {
  const text = typeof data === 'string' ? data : new TextDecoder().decode(data);
  if (hasDuplicateKeys(text)) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return null;
  }
  if (typeof parsed !== 'object' || parsed === null) return null;
  const keys = Object.keys(parsed);
  if (keys.length !== 3 || !keys.every((k) => ['v', 'test', 'nonce'].includes(k))) return null;
  const p = parsed as { v?: unknown; test?: unknown; nonce?: unknown };
  if (p.v !== 1 || p.test !== true || typeof p.nonce !== 'string' || p.nonce.length !== 43) return null;
  return { v: 1, test: true, nonce: p.nonce };
}
```

In `handlePush`, record the wake-up receipt after the `seq` is persisted:

```ts
  await markPushReceived(company.origin, Date.now());
```

In `app/src/sw.ts`, dispatch the test outcome before showing a notification:

```ts
      if (!outcome.accepted) return;
      if (outcome.test) return; // silent record: the app shows the result
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd app && npx vitest run src/lib/relay-sw.test.ts`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add app/src/lib/relay-sw.ts app/src/sw.ts app/src/lib/relay-sw.test.ts
git commit -m "feat(app): handle the self-test payload in the service worker"
```

---

### Task 8: The first-company screen and the notification banner

**Files:**
- Create: `app/src/ui/NotificationBanner.tsx`
- Modify: `app/src/ui/Contacts.tsx`, `app/src/ui/Company.tsx`, `app/src/ui/AddCompany.tsx`
- Modify: `app/src/state.tsx` (actions)
- Test: manual (the app has no component test runner; the state machine is unit-tested in Task 6)

- [ ] **Step 1: Add the actions to `app/src/state.tsx`**

```ts
      async enableNotifications() {
        if (permissionState() === 'unsupported') return false;
        if ((await Notification.requestPermission()) !== 'granted') return false;
        const result = await runSelfTest(companies);
        setNotification(await notificationState());
        return result.endpoint === 'delivered';
      },
      async checkNotifications() {
        setNotification(await notificationState());
      },
      async runNotificationSelfTest() {
        const result = await runSelfTest(companies);
        setNotification(await notificationState());
        return result;
      },
```

Add `notification: NotificationState` to `AppContextValue` and initialise it in `AppProvider`:

```ts
  const [notification, setNotification] = useState<NotificationState>({ kind: 'unsupported' });
```

```ts
      void notificationState().then(setNotification);
```

Add to `AppActions`:

```ts
  /** ask for permission, register, self-test; false when it failed */
  enableNotifications: () => Promise<boolean>;
  /** re-read permission/subscription/registration state */
  checkNotifications: () => Promise<void>;
  /** re-run the self-test without prompting */
  runNotificationSelfTest: () => Promise<SelfTestResult>;
```

Replace the old `enableNotifications(origin)` implementation (the per-company version) entirely.

- [ ] **Step 2: Write the banner component**

```tsx
/**
 * App-wide notification status banner (design/notifications.md): red when
 * wake-ups are off, neutral while testing, green on success. The green state
 * auto-dismisses into the app-wide card.
 */
import { useEffect, useState } from 'react';
import type { NotificationState } from '../lib/notify';

export function NotificationBanner({
  state,
  onEnable,
  onCheck,
}: {
  state: NotificationState;
  onEnable: () => void;
  onCheck: () => void;
}) {
  const [dismissed, setDismissed] = useState(false);
  const ok = state.kind === 'ok';
  useEffect(() => {
    if (!ok) {
      setDismissed(false);
      return;
    }
    const t = setTimeout(() => setDismissed(true), 6000);
    return () => clearTimeout(t);
  }, [ok, state.testedAt]);

  if (state.kind === 'unsupported') {
    return (
      <div className="banner banner-neutral">
        Wake-ups are unavailable in this browser — messages still arrive by polling.
      </div>
    );
  }
  if (state.kind === 'ok') {
    if (dismissed) {
      return (
        <div className="banner banner-subtle">
          Wake-ups on{state.testedAt ? ` · tested ${new Date(state.testedAt).toLocaleTimeString()}` : ''}
        </div>
      );
    }
    return <div className="banner banner-ok">Notifications are working.</div>;
  }
  const message =
    state.kind === 'denied'
      ? 'Notifications are off. Allow them in your browser or system settings, then check again.'
      : state.kind === 'default'
        ? 'Turn on notifications to get timely updates.'
        : 'Wake-ups need to be re-enabled.';
  return (
    <div className="banner banner-danger">
      <span>{message}</span>
      <button className="btn btn-primary" onClick={state.kind === 'default' ? onEnable : onCheck}>
        {state.kind === 'default' ? 'Turn on' : 'Check again'}
      </button>
    </div>
  );
}
```

Match the app's existing class names (`banner`, `btn btn-primary`) from `app/src/styles.css`; if the app uses different banner classes, reuse the existing alert classes instead.

- [ ] **Step 3: Mount the banner in `Contacts.tsx`**

```tsx
      <div className="screen-pad" style={{ paddingTop: 8 }}>
        <NotificationBanner state={notification} onEnable={() => void actions.enableNotifications()} onCheck={() => void actions.checkNotifications()} />
```

Add `notification: NotificationState` and `actions` to the `Contacts` props (pass them from `App.tsx` like `companies`/`items`).

- [ ] **Step 4: Drop the per-company chip in `Company.tsx`**

Remove the `Notifications` section and its chip from `SettingsSheet` (the app-wide banner replaces it). Keep the settings sheet's other rows.

- [ ] **Step 5: Add the first-company screen in `AddCompany.tsx`**

Add the step to the union:

```tsx
  | { t: 'notifications'; origin: string }
```

After `subscribe(followed)` succeeds, set the step instead of finishing:

```ts
    if (repairOrigin) {
      onDone(company.origin);
      return;
    }
    setStep({ t: 'notifications', origin: company.origin });
```

Render the screen (no skip):

```tsx
  if (step.t === 'notifications') {
    return (
      <NotificationsScreen
        onEnable={() => void actions.enableNotifications()}
        onDone={() => onDone(step.origin)}
      />
    );
  }
```

```tsx
function NotificationsScreen({ onEnable, onDone }: { onEnable: () => void; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  return (
    <div className="screen screen-pad">
      <h1>Turn on notifications</h1>
      <p>
        Timely updates — security incidents and order status — reach this device only
        with notifications on.
      </p>
      {busy ? (
        <p className="t-small">Setting up wake-ups…</p>
      ) : failed ? (
        <p className="alert alert-danger">
          Notifications are off. Allow them in your browser or system settings, then try
          again.
        </p>
      ) : null}
      <button
        className="btn btn-primary"
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          const ok = await onEnable();
          setBusy(false);
          if (ok) onDone();
          else setFailed(true);
        }}
      >
        Turn on
      </button>
    </div>
  );
}
```

The second-company skip: `subscribe(followed)` checks `permissionState() === 'granted'` and the registration is current before choosing the step:

```ts
    const permission = permissionState();
    if (permission === 'granted' && (await registrationCurrent(company))) {
      await actions.runNotificationSelfTest(); // silent: no prompt is possible
      onDone(company.origin);
      return;
    }
```

`registrationCurrent(company)` is `ensureRelayRegistration([company])` followed by a `pendingTest`/`unionTopics` check; import it from `relay-sw.ts` and keep it small:

```ts
// imports in AddCompany.tsx
import { getRegistration } from '../lib/store';
import { relayBaseUrl } from '../lib/relay';
import { topicBindings } from '../lib/relay-sw';
import { permissionState } from '../lib/notify';

/** Whether this company's topics are already registered on the relay. */
async function registrationCurrent(company: CompanyRecord): Promise<boolean> {
  const base = relayBaseUrl();
  if (!base) return false;
  const relay = await getRegistration(base);
  if (!relay) return false;
  const topics = Object.keys(topicBindings(company));
  return topics.every((t) => t in relay.topics);
}
```

- [ ] **Step 6: Run the whole app suite**

Run: `cd app && npx vitest run && npx tsc --noEmit`
Expected: PASS, no type errors

- [ ] **Step 7: Verify manually against the local harness**

```sh
# terminal 1
cd relay && go run ./cmd/relay-harness -keys ../keryx-demo-keys
# terminal 2
cd app && VITE_BASE=/ VITE_RELAY_URL=http://127.0.0.1:18099 VITE_VAPID_PUBLIC=<from /test/info> npm run build && npx vite preview --port 4173
# browser: http://localhost:4173/ → pair → "Turn on notifications" → allow
# then
curl -X POST http://127.0.0.1:18099/v1/registrations/<id>/test   # needs the management token
```

Expected: the "Turn on notifications" screen appears after channel selection; after allowing, progress then green; the relay log shows the self-test delivery.

- [ ] **Step 8: Commit**

```bash
git add app/src/ui/NotificationBanner.tsx app/src/ui/Contacts.tsx app/src/ui/Company.tsx app/src/ui/AddCompany.tsx app/src/state.ts app/src/styles.css
git commit -m "feat(app): first-company turn-on screen and the notification banner"
```

---

### Task 9: Extend the relay e2e test with the self-test path

**Files:**
- Modify: `relay/internal/e2e/e2e_test.go`
- Test: `relay/internal/e2e/e2e_test.go`

- [ ] **Step 1: Add the self-test to the existing end-to-end test**

After the replay-suppression assertion, register the fake push service's subscription (already registered in step 3 of the test) and call the test endpoint with its management token:

```go
	// 7. The self-test (§5.3.1) delivers the §4.3 payload to the same
	// registration: never a wake-up, never a sequence change.
	fake.mu.Lock()
	fake.body = nil
	fake.headers = nil
	fake.mu.Unlock()
	rec = post(t, handler, "/v1/registrations/"+registrationID+"/test", "", map[string]string{
		"Authorization": "Bearer " + managementToken,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("self-test = %d: %s", rec.Code, rec.Body)
	}
	var testResult struct {
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &testResult); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fake.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the self-test reached no push service")
	}
	fake.mu.Lock()
	testBody := fake.body
	fake.mu.Unlock()
	plain = decryptRFC8291(t, uaPriv, auth, testBody)
	var testPayload struct {
		V     int    `json:"v"`
		Test  bool   `json:"test"`
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(plain, &testPayload); err != nil {
		t.Fatal(err)
	}
	if testPayload.V != 1 || !testPayload.Test || testPayload.Nonce != testResult.Nonce {
		t.Fatalf("self-test payload = %s", plain)
	}
```

Capture `registrationID`/`managementToken` from the registration response in step 3 (extend the existing decode there).

- [ ] **Step 2: Run the test to verify it passes**

Run: `cd relay && go test -count=1 -run TestEndToEndDemoRepository ./internal/e2e/ -v`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add relay/internal/e2e/e2e_test.go
git commit -m "test(relay): cover the self-test in the end-to-end path"
```

---

### Task 10: Full verification

- [ ] **Step 1: Run every Go suite**

Run: `cd relay && go vet ./... && go test ./...`
Expected: PASS

- [ ] **Step 2: Run the app suite and type check**

Run: `cd app && npx tsc --noEmit && npx vitest run`
Expected: PASS

- [ ] **Step 3: Run the make targets**

Run: `make relay-test relay-e2e app-test`
Expected: PASS

- [ ] **Step 4: Commit any fixes**

```bash
git add -A
git commit -m "chore: final verification fixes"
```

---

## Out of scope (separate plan)

The FCM topic-leg self-test — the relay-generated topic handshake (`POST /v1/fcm/test`, `…/{test_id}/ready`) and the native shell's FCM topic subscribe — needs the native shell, which does not exist yet. The relay API for it is specified in `relay/SPECIFICATION.md` §5.3.1 and waits for that plan.

## Self-review notes

- Spec coverage: §4.3 payload → Task 1; §5.3.1 endpoint leg → Tasks 2/3/5/6/7; §5.3.1 rate limits → Task 3; app-wide registration → Task 4; state machine + first-company screen + banner → Tasks 6/8; conformance ("a test is never a wake-up", nonce binding) → Tasks 3/7/9.
- Type consistency: `RelayRegistration` (existing, `relay.ts`) is reused as the persisted record; `NotificationState`/`SelfTestResult` (`notify.ts`), `PendingTest` (`store.ts`), `testRegistration` (`relay.ts`), `SendToEndpoint`/`ErrEndpointLegDisabled` (`dispatch.go`), `RegistrationForManagement` (`store.go`) are used consistently across tasks.
- Two deliberate spec-level details to confirm during implementation: the `410` response for a dead endpoint in Task 3 (the spec's error list in §5.3.1 does not yet name it), and the `originOfTopic` helper in Task 6, which the plan resolves by matching company `topicBindings` against the registration's topics.
