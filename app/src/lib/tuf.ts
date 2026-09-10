/**
 * TUF metadata — parsing, signature verification and full-chain walking.
 * This is the Keryx TUF client (spec/clients.md §1): root anchor
 * (well-known on the join origin) → root chain → timestamp → snapshot →
 * targets → the followed channels' delegated role metadata
 * (`channels.<name>.json`) → hash-pinned feed target files.
 *
 * The metadata chain is verified with OLPC canonical JSON (the TUF library
 * convention, spec/core.md §1); feed items use JCS (item.ts). The standard
 * TUF 1.0 client workflow is implemented in-repo because the browser has no
 * TUF client (tuf-js is Node-only).
 */

import { olpcCanonical } from './olpc';
import { hexToBytes, bytesToHex, sha256Hex } from './bytes';
import { ed25519Verify } from './ed';
import { pathPatternMatches } from './pattern';

// ---------------------------------------------------------------------------
// Types (mirror the wire format)
// ---------------------------------------------------------------------------

export interface TufKey {
  keytype: string;
  scheme: string;
  keyval: { public: string };
  [extra: string]: unknown;
}

export interface TufRole {
  keyids: string[];
  threshold: number;
}

export interface TufSignature {
  keyid: string;
  sig: string;
  [extra: string]: unknown;
}

export interface RootSigned {
  _type: string;
  spec_version: string;
  version: number;
  expires: string;
  consistent_snapshot?: boolean;
  keys: Record<string, TufKey>;
  roles: Record<string, TufRole>;
  custom?: { repo_base?: string; mirrors?: string[]; mode?: 'full' | 'lite' };
}

export interface RootDoc {
  signatures: TufSignature[];
  signed: RootSigned;
}

export interface MetaInfo {
  version: number;
  length?: number;
  hashes?: Record<string, string>;
}

export interface TimestampSigned {
  _type: string;
  spec_version: string;
  version: number;
  expires: string;
  meta: Record<string, MetaInfo>;
}

export interface TimestampDoc {
  signatures: TufSignature[];
  signed: TimestampSigned;
}

export interface SnapshotSigned {
  _type: string;
  spec_version: string;
  version: number;
  expires: string;
  meta: Record<string, MetaInfo>;
}

export interface SnapshotDoc {
  signatures: TufSignature[];
  signed: SnapshotSigned;
}

export interface TargetInfo {
  length: number;
  hashes: Record<string, string>;
  custom?: unknown;
}

export interface DelegatedRole {
  name: string;
  keyids: string[];
  threshold: number;
  paths?: string[];
  terminating?: boolean;
}

export interface TargetsSigned {
  _type: string;
  spec_version: string;
  version: number;
  expires: string;
  targets: Record<string, TargetInfo>;
  delegations?: { keys: Record<string, TufKey>; roles: DelegatedRole[] };
  custom?: {
    company_name?: string;
    logo?: string;
    logo_sha256?: string;
    editor_mode?: Record<string, EditorModeEntry>;
    private_feed_patterns?: PrivateFeedPattern[];
  };
}

export interface TargetsDoc {
  signatures: TufSignature[];
  signed: TargetsSigned;
}

export interface EditorModeEntry {
  keys: Record<string, TufKey>;
  keyids: string[];
  threshold: number;
}

export interface PrivateFeedPattern {
  channel: string;
  pattern: string;
  keys: Record<string, TufKey>;
  keyids: string[];
  threshold: number;
  display_name?: string;
  purpose?: string;
}

/** Client-side version memory (anti-rollback): last seen versions per role. */
export interface SeenVersions {
  timestamp?: number;
  snapshot?: number;
  targets?: number;
  /** per delegated role (channel): last seen `channels.<name>.json` version */
  roles?: Record<string, number>;
}

// ---------------------------------------------------------------------------
// Errors — three classes, three outcomes (spec/core.md §2, §4)
// ---------------------------------------------------------------------------

/** Verification failure: metadata/content does not verify → reject. */
export class ProtocolError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ProtocolError';
  }
}

/** Transport failure (HTTP status, network, unparseable JSON): NOT verification → retry, never suspension. */
export class FetchError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'FetchError';
  }
}

/** Chain break: a validly-signed root change that cannot be linked to the pinned anchor → suspension. */
export class ChainBreakError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ChainBreakError';
  }
}

export type FetchLike = (url: string) => Promise<Response>;

// ---------------------------------------------------------------------------
// Signature verification
// ---------------------------------------------------------------------------

function verifyEd25519(pubHex: string, msgBytes: Uint8Array, sigHex: string): boolean {
  try {
    return ed25519Verify(hexToBytes(sigHex), msgBytes, hexToBytes(pubHex));
  } catch {
    return false;
  }
}

/**
 * Verify that `signatures` meet `role.threshold` with distinct keys from
 * `role.keyids` (resolved in `keys`), over the OLPC-canonical form of
 * `signed`. Unknown keyids are not role keys and are skipped; a known keyid
 * with a failing signature never counts.
 */
export function verifyRole(
  signed: unknown,
  role: TufRole,
  keys: Record<string, TufKey>,
  signatures: TufSignature[],
): { ok: boolean; valid: string[]; reason?: string } {
  const canonical = new TextEncoder().encode(olpcCanonical(signed));
  const valid = new Set<string>();
  const problems: string[] = [];
  for (const sig of signatures) {
    if (!role.keyids.includes(sig.keyid)) continue;
    const key = keys[sig.keyid];
    if (!key) {
      problems.push(`key ${sig.keyid} not found in metadata`);
      continue;
    }
    if (key.keytype !== 'ed25519') {
      problems.push(`unsupported keytype ${key.keytype}`);
      continue;
    }
    if (verifyEd25519(key.keyval.public, canonical, sig.sig)) {
      valid.add(sig.keyid);
    }
  }
  const ok = valid.size >= role.threshold;
  return {
    ok,
    valid: [...valid],
    reason: ok ? undefined : problems.join('; ') || `only ${valid.size}/${role.threshold} valid signatures`,
  };
}

/**
 * TUF-standard keyid check (spec/core.md §1): the keyid is the SHA-256 hex
 * of the canonical key object {keytype, scheme, keyval}. A mismatch is a
 * metadata error → reject (the key object must match the keyid it is
 * published under).
 */
export function verifyKeyid(keyid: string, key: TufKey, where: string): void {
  if (key.keytype !== 'ed25519' || key.scheme !== 'ed25519') {
    throw new ProtocolError(`${where}: unsupported key ${key.keytype}/${key.scheme}`);
  }
  const canonical = olpcCanonical({
    keytype: key.keytype,
    scheme: key.scheme,
    keyval: key.keyval,
  });
  const got = sha256Hex(new TextEncoder().encode(canonical));
  if (got !== keyid) {
    throw new ProtocolError(`${where}: keyid does not match key object`);
  }
}

export function verifyRoot(root: RootDoc): void {
  const role = root.signed.roles.root;
  if (!role) throw new ProtocolError('root.json: missing "root" role');
  const res = verifyRole(root.signed, role, root.signed.keys, root.signatures);
  if (!res.ok) throw new ProtocolError(`root.json: self-signature invalid (${res.reason})`);
}

export function verifyTargets(root: RootDoc, targets: TargetsDoc): void {
  const role = root.signed.roles.targets;
  if (!role) throw new ProtocolError('root.json: missing "targets" role');
  const res = verifyRole(targets.signed, role, root.signed.keys, targets.signatures);
  if (!res.ok) throw new ProtocolError(`targets.json: signature by targets role invalid (${res.reason})`);
}

export function verifyTimestamp(root: RootDoc, timestamp: TimestampDoc): void {
  const role = root.signed.roles.timestamp;
  if (!role) throw new ProtocolError('root.json: missing "timestamp" role');
  const res = verifyRole(timestamp.signed, role, root.signed.keys, timestamp.signatures);
  if (!res.ok) throw new ProtocolError(`timestamp.json: signature by timestamp role invalid (${res.reason})`);
}

export function verifySnapshot(root: RootDoc, snapshot: SnapshotDoc): void {
  const role = root.signed.roles.snapshot;
  if (!role) throw new ProtocolError('root.json: missing "snapshot" role');
  const res = verifyRole(snapshot.signed, role, root.signed.keys, snapshot.signatures);
  if (!res.ok) throw new ProtocolError(`snapshot.json: signature by snapshot role invalid (${res.reason})`);
}

/**
 * Root transition (TUF spec, update root role): the new root (version+1)
 * MUST be signed by the previous root's keys per the previous threshold
 * (step 4(1)) AND self-signed by the new root role (step 4(2)).
 */
export function verifyRootTransition(prev: RootDoc, next: RootDoc): void {
  if (next.signed.version !== prev.signed.version + 1) {
    throw new ChainBreakError(
      `root.json: version ${next.signed.version} != expected ${prev.signed.version + 1}`,
    );
  }
  const prevRole = prev.signed.roles.root;
  if (!prevRole) throw new ChainBreakError('root.json: previous root has no root role');
  const res = verifyRole(next.signed, prevRole, prev.signed.keys, next.signatures);
  if (!res.ok) {
    throw new ChainBreakError(`root.json: v${next.signed.version} not signed by previous root keys (${res.reason})`);
  }
  try {
    verifyRoot(next);
  } catch (err) {
    // a root that is not self-signed by its new keys is unverifiable → chain break
    throw new ChainBreakError(`root.json: v${next.signed.version} not self-signed by new root keys (${(err as Error).message})`);
  }
}

/** Validate every key entry's keyid (metadata error → reject). */
export function checkKeyids(keys: Record<string, TufKey>, where: string): void {
  for (const [keyid, key] of Object.entries(keys)) {
    verifyKeyid(keyid, key, where);
  }
}

// ---------------------------------------------------------------------------
// Fetching and chain walking
// ---------------------------------------------------------------------------

async function fetchBytes(fetchFn: FetchLike, url: string): Promise<Uint8Array> {
  let res: Response;
  try {
    res = await fetchFn(url);
  } catch (err) {
    throw new FetchError(`fetch ${url}: ${(err as Error).message}`);
  }
  if (!res.ok) throw new FetchError(`fetch ${url}: HTTP ${res.status}`);
  return new Uint8Array(await res.arrayBuffer());
}

/** Fetch + hash/length-verify one metadata file against its pin (anti-tamper). */
export async function fetchVerifiedBytes(
  fetchFn: FetchLike,
  url: string,
  info: Pick<MetaInfo, 'length' | 'hashes'>,
): Promise<Uint8Array> {
  const bytes = await fetchBytes(fetchFn, url);
  if (info.length !== undefined && bytes.length !== info.length) {
    throw new FetchError(`fetch ${url}: length ${bytes.length} != pinned ${info.length}`);
  }
  const want = info.hashes?.sha256;
  if (want !== undefined) {
    const got = sha256Hex(bytes);
    if (got !== want) throw new FetchError(`fetch ${url}: sha256 mismatch`);
  }
  return bytes;
}

export function versionedRootUrl(anchorUrl: string, version: number): string {
  const u = new URL(anchorUrl);
  const last = u.pathname.split('/').pop() ?? '';
  u.pathname = u.pathname.slice(0, -last.length) + `${version}.${last}`;
  return u.toString();
}

export async function fetchRootDoc(fetchFn: FetchLike, url: string): Promise<RootDoc> {
  const bytes = await fetchBytes(fetchFn, url);
  let doc: unknown;
  try {
    doc = JSON.parse(new TextDecoder().decode(bytes));
  } catch {
    throw new FetchError(`fetch ${url}: unparseable JSON`);
  }
  if (
    typeof doc !== 'object' ||
    doc === null ||
    (doc as RootDoc).signed?._type !== 'root'
  ) {
    throw new FetchError(`fetch ${url}: not a root document`);
  }
  return doc as RootDoc;
}

/**
 * Fetch a metadata file from the repo base, honoring consistent_snapshot:
 * with the flag on, metadata is served at `<version>.<name>` (fall back to
 * the plain path on 404/403 — some static hosts), otherwise at the plain
 * path (spec/repository.md §1).
 */
export async function fetchMetadataBytes(
  fetchFn: FetchLike,
  base: string,
  filename: string,
  consistent: boolean,
  info: MetaInfo,
): Promise<Uint8Array> {
  const expected = { length: info.length, hashes: info.hashes };
  if (consistent) {
    const u = new URL(filename, base);
    const last = u.pathname.split('/').pop() ?? '';
    u.pathname = u.pathname.slice(0, -last.length) + `${info.version}.${last}`;
    try {
      return await fetchVerifiedBytes(fetchFn, u.toString(), expected);
    } catch (err) {
      if (err instanceof FetchError && /HTTP 404|HTTP 403/.test(err.message)) {
        // fall through to the plain path
      } else {
        throw err;
      }
    }
  }
  return fetchVerifiedBytes(fetchFn, new URL(filename, base).toString(), expected);
}

function isExpired(expires: string): boolean {
  const t = Date.parse(expires);
  return !Number.isNaN(t) && t < Date.now();
}

export interface MetadataResult {
  root: RootDoc;
  timestamp: TimestampDoc;
  snapshot: SnapshotDoc;
  targets: TargetsDoc;
  base: string;
  rootRotated: boolean;
  /** any role metadata is expired (stale-but-verified → keep cache, retry; not suspension) */
  stale: boolean;
}

/**
 * Fetch and verify the TUF chain up to targets: root (walked from the
 * pinned anchor) → timestamp → snapshot → targets. Channel role metadata is
 * loaded on demand by loadChannelRole. Throws ChainBreakError on an
 * unverifiable root change (caller suspends); ProtocolError on any other
 * verification failure; FetchError on transport problems (never suspension).
 * Expired metadata does NOT throw: it is reported via `stale` (expiry ≠
 * suspension, spec/repository.md §5).
 */
export async function loadAndVerifyMetadata(
  fetchFn: FetchLike,
  anchorUrl: string,
  pinnedRoot: RootDoc | null,
  seen: SeenVersions | null,
): Promise<MetadataResult> {
  // root: TOFU on first pair; chain-walk afterwards (spec/core.md §3)
  const latest = await fetchRootDoc(fetchFn, anchorUrl);
  let root: RootDoc;
  let rootRotated = false;
  if (!pinnedRoot) {
    verifyRoot(latest);
    root = latest;
  } else if (latest.signed.version <= pinnedRoot.signed.version) {
    // anti-rollback: never accept an older root than the one pinned
    root = pinnedRoot;
  } else {
    // walk the chain from the pinned root to the latest — all root metadata
    // comes EXCLUSIVELY from the well-known anchor (spec/repository.md §1)
    let prev = pinnedRoot;
    for (let v = pinnedRoot.signed.version + 1; v <= latest.signed.version; v++) {
      const doc = await fetchRootDoc(fetchFn, versionedRootUrl(anchorUrl, v));
      if (doc.signed.version !== v) {
        throw new ChainBreakError(`root version ${v} does not match its URL`);
      }
      verifyRootTransition(prev, doc);
      prev = doc;
    }
    root = prev;
    rootRotated = true;
  }

  // repo base: master-signed root.json custom.repo_base (single URL)
  const base = root.signed.custom?.repo_base;
  if (!base) throw new ProtocolError('root.json: custom.repo_base missing');
  // the client MUST understand the mode flag so a lite repo never breaks a
  // full-mode client (spec/clients.md §3): lite verification is a Phase 2
  // path — refuse gracefully (cached content kept, not suspension).
  if (root.signed.custom?.mode === 'lite') {
    throw new FetchError('lite-mode repository is not supported by this app version yet');
  }
  const consistent = root.signed.consistent_snapshot === true;
  let stale = isExpired(root.signed.expires);

  // --- timestamp: unversioned, short expiry (anti-freeze)
  const timestamp = JSON.parse(
    new TextDecoder().decode(await fetchBytes(fetchFn, new URL('timestamp.json', base).toString())),
  ) as TimestampDoc;
  if (timestamp.signed._type !== 'timestamp') throw new ProtocolError('timestamp.json: not a timestamp document');
  verifyTimestamp(root, timestamp);
  if (seen?.timestamp !== undefined && timestamp.signed.version < seen.timestamp) {
    throw new ProtocolError(
      `timestamp.json: version ${timestamp.signed.version} is older than ${seen.timestamp} (rollback?)`,
    );
  }
  stale = stale || isExpired(timestamp.signed.expires);

  // --- snapshot: pinned by timestamp
  const snapshotInfo = timestamp.signed.meta['snapshot.json'];
  if (!snapshotInfo) throw new ProtocolError('timestamp.json: meta.snapshot.json missing');
  const snapshot = JSON.parse(
    new TextDecoder().decode(await fetchMetadataBytes(fetchFn, base, 'snapshot.json', consistent, snapshotInfo)),
  ) as SnapshotDoc;
  if (snapshot.signed._type !== 'snapshot') throw new ProtocolError('snapshot.json: not a snapshot document');
  verifySnapshot(root, snapshot);
  if (snapshot.signed.version !== snapshotInfo.version) {
    throw new ProtocolError(
      `snapshot.json: version ${snapshot.signed.version} != timestamp reference ${snapshotInfo.version}`,
    );
  }
  if (seen?.snapshot !== undefined && snapshot.signed.version < seen.snapshot) {
    throw new ProtocolError(
      `snapshot.json: version ${snapshot.signed.version} is older than ${seen.snapshot} (rollback?)`,
    );
  }
  stale = stale || isExpired(snapshot.signed.expires);

  // --- targets: pinned by snapshot, signed by the targets role
  const targetsInfo = snapshot.signed.meta['targets.json'];
  if (!targetsInfo) throw new ProtocolError('snapshot.json: meta.targets.json missing');
  const targets = JSON.parse(
    new TextDecoder().decode(await fetchMetadataBytes(fetchFn, base, 'targets.json', consistent, targetsInfo)),
  ) as TargetsDoc;
  if (targets.signed._type !== 'targets') throw new ProtocolError('targets.json: not a targets document');
  verifyTargets(root, targets);
  checkKeyids(root.signed.keys, 'root.json keys');
  checkKeyids(targets.signed.delegations?.keys ?? {}, 'targets.json delegations');
  if (targets.signed.version !== targetsInfo.version) {
    throw new ProtocolError(
      `targets.json: version ${targets.signed.version} != snapshot reference ${targetsInfo.version}`,
    );
  }
  if (seen?.targets !== undefined && targets.signed.version < seen.targets) {
    throw new ProtocolError(
      `targets.json: version ${targets.signed.version} is older than ${seen.targets} (rollback?)`,
    );
  }
  stale = stale || isExpired(targets.signed.expires);

  return { root, timestamp, snapshot, targets, base, rootRotated, stale };
}

/**
 * Fetch and verify one channel's delegated role metadata
 * (`channels.<name>.json`): signed by the delegation role keys (from
 * targets.json), pinned by snapshot.json (version + hashes), anti-rollback
 * via `seenRole`. Also checks that every target path the role pins stays
 * inside the role's delegation namespace (TUF delegated-targets rule).
 */
export async function loadChannelRole(
  fetchFn: FetchLike,
  meta: Pick<MetadataResult, 'base' | 'snapshot' | 'targets'>,
  roleName: string,
  consistent: boolean,
  seenRole?: number,
): Promise<TargetsDoc> {
  const role = meta.targets.signed.delegations?.roles.find((r) => r.name === roleName);
  if (!role) throw new ProtocolError(`targets.json: missing delegation "${roleName}"`);
  const info = meta.snapshot.signed.meta[`${roleName}.json`];
  if (!info) throw new ProtocolError(`snapshot.json: meta.${roleName}.json missing (delegated role present)`);
  const bytes = await fetchMetadataBytes(fetchFn, meta.base, `${roleName}.json`, consistent, info);
  const doc = JSON.parse(new TextDecoder().decode(bytes)) as TargetsDoc;
  if (doc.signed._type !== 'targets') throw new ProtocolError(`${roleName}.json: not a targets document`);
  verifyDelegatedTargets(meta.targets, doc, roleName);
  if (doc.signed.version !== info.version) {
    throw new ProtocolError(
      `${roleName}.json: version ${doc.signed.version} != snapshot reference ${info.version}`,
    );
  }
  if (seenRole !== undefined && doc.signed.version < seenRole) {
    throw new ProtocolError(`${roleName}.json: version ${doc.signed.version} is older than ${seenRole} (rollback?)`);
  }
  // delegated-targets rule: pinned paths must be inside the role's paths
  const paths = role.paths ?? [];
  for (const path of Object.keys(doc.signed.targets)) {
    if (!paths.some((p) => pathPatternMatches(p, path))) {
      throw new ProtocolError(`${roleName}.json: target ${path} outside delegation paths ${paths.join(', ')}`);
    }
  }
  return doc;
}

export function verifyDelegatedTargets(parent: TargetsDoc, child: TargetsDoc, roleName: string): void {
  const role = parent.signed.delegations?.roles.find((r) => r.name === roleName);
  if (!role) throw new ProtocolError(`targets.json: missing delegation "${roleName}"`);
  const res = verifyRole(child.signed, role, parent.signed.delegations!.keys, child.signatures);
  if (!res.ok) throw new ProtocolError(`${roleName}.json: signature by ${roleName} role invalid (${res.reason})`);
}

// ---------------------------------------------------------------------------
// Feed target files
// ---------------------------------------------------------------------------

/**
 * URL of a feed target file. With consistent snapshots the target file is
 * served under its hash: `<sha256>.<basename>` in the same directory (TUF
 * consistent-snapshot naming).
 */
export function targetFileUrl(base: string, path: string, info: TargetInfo, consistent: boolean): string {
  const u = new URL(path, base);
  if (consistent) {
    const hash = info.hashes.sha256;
    if (!hash) throw new ProtocolError(`target ${path}: no sha256 hash for consistent snapshot`);
    const last = u.pathname.split('/').pop() ?? '';
    u.pathname = u.pathname.slice(0, -last.length) + `${hash}.${last}`;
  }
  return u.toString();
}

/** Verify raw target-file bytes against its pinned length + sha256. */
export function verifyTargetBytes(bytes: Uint8Array, info: TargetInfo, path: string): void {
  if (bytes.length !== info.length) {
    throw new ProtocolError(`target ${path}: length ${bytes.length} != expected ${info.length}`);
  }
  const want = info.hashes.sha256;
  if (want !== undefined) {
    const got = sha256Hex(bytes);
    if (got !== want) throw new ProtocolError(`target ${path}: sha256 mismatch`);
  }
}

// ---------------------------------------------------------------------------
// Authorization model (spec/repository.md §2, spec/feeds.md §2–§3)
// ---------------------------------------------------------------------------

export interface AuthorizedKey {
  keyid: string;
  pub: Uint8Array;
}

export interface ChannelAuth {
  /** the bare channel name (from the role name `channels.<name>`) */
  channel: string;
  role: DelegatedRole;
  keys: AuthorizedKey[];
}

export interface EditorAuth {
  keys: AuthorizedKey[];
  threshold: number;
}

export interface Authorization {
  channels: Map<string, ChannelAuth>;
  editor: Map<string, EditorAuth>;
  privatePatterns: PrivateFeedPattern[];
}

const CHANNEL_RE = /^[a-z0-9-_]+$/;

/**
 * Is this delegation a public channel role? Per spec/repository.md §2 the
 * role name MUST be `channels.<channel>` and the app MUST ignore roles not
 * beginning with `channels.` and roles whose paths fall outside their own
 * `channels/<channel>/*` namespace.
 */
export function channelFromRole(role: DelegatedRole): string | null {
  if (!role.name.startsWith('channels.')) return null;
  const channel = role.name.slice('channels.'.length);
  if (!CHANNEL_RE.test(channel)) return null;
  // paths must stay inside the role's own channels/<name>/* namespace
  const ns = `channels/${channel}/*`;
  const paths = role.paths ?? [];
  if (paths.length === 0) return null;
  if (!paths.every((p) => pathPatternMatches(ns, p))) return null;
  return channel;
}

function resolveKeys(keys: Record<string, TufKey>, keyids: string[], where: string): AuthorizedKey[] {
  const out: AuthorizedKey[] = [];
  for (const keyid of keyids) {
    const key = keys[keyid];
    // key publication rule (spec/repository.md §2): a keyid without its key
    // object is a metadata error → reject.
    if (!key) throw new ProtocolError(`${where}: key object for ${keyid} missing`);
    if (key.keytype !== 'ed25519') throw new ProtocolError(`${where}: unsupported keytype ${key.keytype}`);
    out.push({ keyid, pub: hexToBytes(key.keyval.public) });
  }
  return out;
}

export function extractAuthorization(targets: TargetsDoc): Authorization {
  const auth: Authorization = { channels: new Map(), editor: new Map(), privatePatterns: [] };
  const dlg = targets.signed.delegations;
  if (dlg) {
    checkKeyids(dlg.keys, 'targets.json delegations');
    for (const role of dlg.roles) {
      const channel = channelFromRole(role);
      if (!channel) continue; // not a channel — ignored, namespace stays open
      const keys = resolveKeys(dlg.keys, role.keyids, `delegation ${role.name}`);
      auth.channels.set(channel, { channel, role, keys });
    }
  }
  for (const [channel, entry] of Object.entries(targets.signed.custom?.editor_mode ?? {})) {
    if (!entry) continue;
    checkKeyids(entry.keys, `editor_mode.${channel}`);
    const keys = resolveKeys(entry.keys, entry.keyids, `editor_mode.${channel}`);
    // key separation (spec/feeds.md §2): editor keyids MUST NOT be the channel role keyid
    const roleKey = auth.channels.get(channel)?.role.keyids ?? [];
    for (const kid of entry.keyids) {
      if (roleKey.includes(kid)) {
        throw new ProtocolError(`editor_mode.${channel}: key ${kid} is also the channel role key`);
      }
    }
    auth.editor.set(channel, { keys, threshold: Math.max(1, entry.threshold || 1) });
  }
  for (const entry of targets.signed.custom?.private_feed_patterns ?? []) {
    if (!entry) continue;
    checkKeyids(entry.keys, `private_feed_patterns.${entry.channel}`);
    resolveKeys(entry.keys, entry.keyids, `private_feed_patterns.${entry.channel}`);
    auth.privatePatterns.push(entry);
  }
  return auth;
}
