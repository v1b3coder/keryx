package cz.v1b3coder.keryx;

import android.util.Base64;

import org.bouncycastle.crypto.params.Ed25519PublicKeyParameters;
import org.bouncycastle.crypto.signers.Ed25519Signer;
import org.json.JSONArray;
import org.json.JSONObject;

import java.nio.charset.StandardCharsets;
import java.util.HashMap;
import java.util.Map;

/**
 * The §4 envelope parse and verification against the mirrored state
 * (relay/SPECIFICATION.md §4.1–4.2). The mirror is pushed by the JS layer
 * from its TUF-verified state (verify-state.ts): a malicious relay cannot
 * forge keys, only replay a wake-up (dropped by `seq`).
 *
 * The worker fetches no TUF metadata and no content: {@link #verify} reads only
 * this in-memory mirror. A payload that fails verification is queued for the page
 * anyway, so a stale mirror costs a delayed notice, never a lost wake-up.
 */
public final class WakeupVerify {
    private static final String DOMAIN = "keryx/wakeup/v1|";

    public static final class TopicState {
        public final String[] keyids;
        public final byte[][] pubs;
        public final String[] pubHex;
        public final int threshold;
        public long lastSeq;

        public TopicState(String[] keyids, byte[][] pubs, String[] pubHex, int threshold, long lastSeq) {
            this.keyids = keyids;
            this.pubs = pubs;
            this.pubHex = pubHex;
            this.threshold = threshold;
            this.lastSeq = lastSeq;
        }
    }

    private final Map<String, TopicState> topics = new HashMap<>();

    /** Replace the whole mirror (the JS layer pushes after every change). */
    public synchronized void setState(JSONObject state) {
        topics.clear();
        if (state == null) return;
        JSONObject topicsJson = state.optJSONObject("topics");
        if (topicsJson == null) return;
        java.util.Iterator<String> topicKeys = topicsJson.keys();
        while (topicKeys.hasNext()) {
            String topic = topicKeys.next();
            JSONObject t = topicsJson.optJSONObject(topic);
            if (t == null) continue;
            JSONArray keys = t.optJSONArray("keys");
            if (keys == null || keys.length() == 0) continue;
            String[] keyids = new String[keys.length()];
            byte[][] pubs = new byte[keys.length()][];
            String[] pubHex = new String[keys.length()];
            boolean valid = true;
            for (int i = 0; i < keys.length(); i++) {
                JSONObject key = keys.optJSONObject(i);
                if (key == null) {
                    valid = false;
                    break;
                }
                keyids[i] = key.optString("keyid", "");
                pubHex[i] = key.optString("pub", "");
                try {
                    pubs[i] = hexToBytes(pubHex[i]);
                } catch (IllegalArgumentException e) {
                    valid = false;
                    break;
                }
                if (keyids[i].isEmpty() || pubs[i] == null || pubs[i].length != 32) {
                    valid = false;
                    break;
                }
            }
            if (!valid) continue;
            topics.put(
                    topic,
                    new TopicState(keyids, pubs, pubHex, Math.max(1, t.optInt("threshold", 1)), t.optLong("lastSeq", 0)));
        }
    }

    /** The mirror as the JS layer pushed it, with every accepted `seq` advanced. */
    public synchronized JSONObject toJson() {
        JSONObject topicsJson = new JSONObject();
        for (Map.Entry<String, TopicState> entry : topics.entrySet()) {
            TopicState state = entry.getValue();
            JSONArray keys = new JSONArray();
            for (int i = 0; i < state.keyids.length; i++) {
                JSONObject key = new JSONObject();
                try {
                    key.put("keyid", state.keyids[i]);
                    key.put("pub", state.pubHex[i]);
                } catch (Exception e) {
                    continue;
                }
                keys.put(key);
            }
            JSONObject topic = new JSONObject();
            try {
                topic.put("keys", keys);
                topic.put("threshold", state.threshold);
                topic.put("lastSeq", state.lastSeq);
                topicsJson.put(entry.getKey(), topic);
            } catch (Exception e) {
                // impossible for a JSONObject of JSON-safe values
            }
        }
        JSONObject out = new JSONObject();
        try {
            out.put("topics", topicsJson);
        } catch (Exception e) {
            // impossible for a JSONObject of JSON-safe values
        }
        return out;
    }

    /**
     * Verify one §4 envelope: strict fields, topic known, Ed25519 threshold,
     * `seq` above the mirror's last accepted value. On acceptance the mirror's
     * `seq` advances, so a replay is dropped even while the app is killed
     * (the mirror itself is persisted by the caller).
     */
    public synchronized boolean verify(String payload) {
        JSONObject envelope;
        try {
            envelope = new JSONObject(payload);
        } catch (Exception e) {
            return false;
        }
        if (envelope.optInt("v", -1) != 1) return false;
        String topic = envelope.optString("t", "");
        long seq = envelope.optLong("seq", -1);
        JSONArray sigs = envelope.optJSONArray("sig");
        if (topic.isEmpty() || seq < 1 || sigs == null || sigs.length() == 0) return false;
        TopicState state = topics.get(topic);
        if (state == null) return false;
        if (seq <= state.lastSeq) return false;
        // the signed bytes are exactly wakeup.go's SignedBytes: the OLPC canonical
        // form sorts keys `seq`, `t`, `v`; the topic is base64url, so no escaping
        byte[] signed = (DOMAIN + "{\"seq\":" + seq + ",\"t\":\"" + topic + "\",\"v\":1}")
                .getBytes(StandardCharsets.UTF_8);
        Map<String, Integer> seen = new HashMap<>();
        int valid = 0;
        for (int i = 0; i < sigs.length(); i++) {
            JSONObject sig = sigs.optJSONObject(i);
            if (sig == null) return false;
            String keyid = sig.optString("keyid", "");
            byte[] raw;
            try {
                raw = base64urlToBytes(sig.optString("sig", ""));
            } catch (IllegalArgumentException e) {
                return false;
            }
            if (raw.length != 64 || keyid.isEmpty() || seen.containsKey(keyid)) continue;
            int keyIndex = -1;
            for (int k = 0; k < state.keyids.length; k++) {
                if (state.keyids[k].equals(keyid)) {
                    keyIndex = k;
                    break;
                }
            }
            if (keyIndex < 0) continue; // unknown keyid is ignored
            seen.put(keyid, keyIndex);
            Ed25519Signer verifier = new Ed25519Signer();
            verifier.init(false, new Ed25519PublicKeyParameters(state.pubs[keyIndex], 0));
            verifier.update(signed, 0, signed.length);
            if (verifier.verifySignature(raw)) valid++;
        }
        if (valid < state.threshold) return false;
        state.lastSeq = seq;
        return true;
    }

    /** 64 hex chars → 32 bytes; throws on anything else. */
    private static byte[] hexToBytes(String hex) {
        if (hex.length() != 64) throw new IllegalArgumentException("not 32-byte hex");
        byte[] out = new byte[32];
        for (int i = 0; i < 32; i++) {
            int hi = Character.digit(hex.charAt(i * 2), 16);
            int lo = Character.digit(hex.charAt(i * 2 + 1), 16);
            if (hi < 0 || lo < 0) throw new IllegalArgumentException("not hex");
            out[i] = (byte) ((hi << 4) | lo);
        }
        return out;
    }

    /** base64url (unpadded) → bytes; throws on malformed input. */
    private static byte[] base64urlToBytes(String value) {
        return Base64.decode(value, Base64.URL_SAFE | Base64.NO_PADDING | Base64.NO_WRAP);
    }
}
