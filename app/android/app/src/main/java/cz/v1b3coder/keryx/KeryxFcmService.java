package cz.v1b3coder.keryx;

import com.google.firebase.messaging.FirebaseMessagingService;
import com.google.firebase.messaging.RemoteMessage;

import java.nio.charset.StandardCharsets;

/**
 * The app side of the FCM topic leg (relay/SPECIFICATION.md §6.1). The relay
 * publishes one data-only message per wake-up: `wakeup` carries the §4 envelope
 * and `test` carries the §4.3 self-test payload. Both go through the same
 * native verification, queue and notice path as the UnifiedPush connector
 * ({@link KeryxPushPlugin#onMessage}), so the JS layer has one path and the
 * native mirror is the same gate either way.
 */
public class KeryxFcmService extends FirebaseMessagingService {
    @Override
    public void onMessageReceived(RemoteMessage message) {
        String wakeup = message.getData().get("wakeup");
        String test = message.getData().get("test");
        String payload = wakeup != null ? wakeup : test;
        if (payload == null) return;
        KeryxPushPlugin.onMessage(getApplicationContext(), payload.getBytes(StandardCharsets.UTF_8));
    }
}
