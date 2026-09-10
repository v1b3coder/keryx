/**
 * Pairing flow (spec/core.md §3, spec/clients.md §1): parse the join URL →
 * (user confirms the origin — the only human step, nothing is fetched before
 * it) → fetch + TOFU-verify the root anchor → run the TUF chain → build the
 * consent summary in the publisher's own signed words → subscribe.
 */

import { parseJoinUrl, rootAnchorUrl, type JoinPayload } from './payload';
import {
  loadAndVerifyMetadata,
  loadChannelRole,
  extractAuthorization,
  ChainBreakError,
  ProtocolError,
  type RootDoc,
  type TargetsDoc,
} from './tuf';
import { channelDisplay } from './sync';
import { matchesPattern } from './private';
import { type CompanyRecord, type ChannelState, type PrivateFeedSub, makeCompany } from './store';

export interface PairingOffer {
  origin: string;
  joinUrl: string;
  companyName: string;
  logo?: string;
  channels: {
    name: string;
    displayName: string;
    description?: string;
    suggested: boolean;
  }[];
  privateFeeds: {
    url: string;
    channel: string;
    displayName?: string;
    purpose?: string;
    /** pattern match failed → rejected (tampered QR), never subscribed */
    valid: boolean;
  }[];
  /** role metadata + targets are cached so subscribe() does not re-fetch */
  _meta: PairingMeta;
}

interface PairingMeta {
  root: RootDoc;
  targets: TargetsDoc;
  roles: Map<string, TargetsDoc>;
  base: string;
  consistent: boolean;
}

export class PairingError extends Error {
  constructor(message: string, public readonly kind: 'verify' | 'network' | 'payload' | 'newer' | 'unsupported') {
    super(message);
    this.name = 'PairingError';
  }
}

/**
 * After the user confirmed the origin: fetch the root anchor (TOFU), verify
 * the chain and build the consent summary. Throws PairingError.
 */
export async function buildPairingOffer(
  origin: string,
  joinUrl: string,
  payload: JoinPayload,
  fetchFn: typeof fetch = fetch,
): Promise<PairingOffer> {
  let meta: Awaited<ReturnType<typeof loadAndVerifyMetadata>>;
  try {
    meta = await loadAndVerifyMetadata(fetchFn, rootAnchorUrl(origin), null, null);
  } catch (err) {
    if (err instanceof ChainBreakError || err instanceof ProtocolError) {
      throw new PairingError(err.message, 'verify');
    }
    throw new PairingError(err instanceof Error ? err.message : String(err), 'network');
  }
  const auth = extractAuthorization(meta.targets);
  const roles = new Map<string, TargetsDoc>();
  for (const [channel] of auth.channels) {
    try {
      roles.set(
        `channels.${channel}`,
        await loadChannelRole(fetchFn, meta, `channels.${channel}`, meta.root.signed.consistent_snapshot === true),
      );
    } catch (err) {
      if (err instanceof ProtocolError) throw new PairingError(err.message, 'verify');
      throw new PairingError(err instanceof Error ? err.message : String(err), 'network');
    }
  }

  const custom = meta.targets.signed.custom;
  const companyName = typeof custom?.company_name === 'string' ? custom.company_name : origin;
  const logo = typeof custom?.logo === 'string' ? custom.logo : undefined;

  const offerChannels = payload.channels.map((name) => {
    const display = channelDisplay(roles.get(`channels.${name}`) ?? null, name);
    return {
      name,
      displayName: display.displayName,
      description: display.description,
      suggested: true,
    };
  });
  // every channel the publisher offers, even if not suggested in the QR
  for (const name of auth.channels.keys()) {
    if (payload.channels.includes(name)) continue;
    const display = channelDisplay(roles.get(`channels.${name}`) ?? null, name);
    offerChannels.push({ name, displayName: display.displayName, description: display.description, suggested: false });
  }

  const privateFeeds = payload.privateFeeds.map((url) => {
    const entry = auth.privatePatterns.find((p) => matchesPattern(p, url));
    return {
      url,
      channel: entry?.channel ?? '',
      displayName: entry?.display_name,
      purpose: entry?.purpose,
      valid: !!entry,
    };
  });

  return {
    origin,
    joinUrl,
    companyName,
    logo,
    channels: offerChannels,
    privateFeeds,
    _meta: {
      root: meta.root,
      targets: meta.targets,
      roles,
      base: meta.base,
      consistent: meta.root.signed.consistent_snapshot === true,
    },
  };
}

/** Create the company record from a confirmed offer (called on Subscribe). */
export function createCompanyFromOffer(offer: PairingOffer, followed: string[]): CompanyRecord {
  const channels: ChannelState[] = offer.channels.map((c) => ({
    name: c.name,
    displayName: c.displayName,
    description: c.description,
    followed: followed.includes(c.name),
    isNew: false,
  }));
  const privateFeeds: PrivateFeedSub[] = offer.privateFeeds
    .filter((f) => f.valid)
    .map((f) => ({
      url: f.url,
      channel: f.channel,
      displayName: f.displayName,
      purpose: f.purpose,
    }));
  return makeCompany(offer.origin, offer.joinUrl, offer._meta.root, offer._meta.targets, {
    companyName: offer.companyName,
    logo: offer.logo,
  }, channels, privateFeeds);
}

export { parseJoinUrl, rootAnchorUrl };
