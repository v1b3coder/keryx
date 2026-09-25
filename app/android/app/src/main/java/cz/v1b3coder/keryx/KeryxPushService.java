package cz.v1b3coder.keryx;

import org.unifiedpush.android.connector.FailedReason;
import org.unifiedpush.android.connector.PushService;
import org.unifiedpush.android.connector.data.PushEndpoint;
import org.unifiedpush.android.connector.data.PushMessage;

/**
 * The app side of the UnifiedPush connector (relay/SPECIFICATION.md §6.3). The
 * connector library handles the distributor protocol and RFC 8291 decryption;
 * this service receives plaintext §4 envelopes.
 *
 * The payload is handed to the plugin, which verifies it against the mirrored
 * verification state and queues it for the JS layer (KeryxPushPlugin).
 */
public class KeryxPushService extends PushService {
    @Override
    public void onNewEndpoint(PushEndpoint endpoint, String instance) {
        KeryxPushPlugin.onNewEndpoint(
                getApplicationContext(),
                endpoint.getUrl(),
                endpoint.getPubKeySet().getPubKey(),
                endpoint.getPubKeySet().getAuth());
    }

    @Override
    public void onMessage(PushMessage message, String instance) {
        KeryxPushPlugin.onMessage(getApplicationContext(), message.getContent());
    }

    @Override
    public void onRegistrationFailed(FailedReason reason, String instance) {
        KeryxPushPlugin.onRegistrationFailed(reason.name());
    }

    @Override
    public void onUnregistered(String instance) {
        KeryxPushPlugin.onUnregistered(getApplicationContext());
    }

    @Override
    public void onTempUnavailable(String instance) {
        KeryxPushPlugin.onTempUnavailable();
    }
}
