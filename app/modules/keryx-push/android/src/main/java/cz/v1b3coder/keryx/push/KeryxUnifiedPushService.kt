package cz.v1b3coder.keryx.push

import org.unifiedpush.android.connector.FailedReason
import org.unifiedpush.android.connector.PushService
import org.unifiedpush.android.connector.data.PushEndpoint
import org.unifiedpush.android.connector.data.PushMessage

/**
 * The app side of the UnifiedPush connector (relay/SPECIFICATION.md §6.3). The
 * connector library handles the distributor protocol and RFC 8291 decryption;
 * this service receives plaintext §4 envelopes.
 *
 * The payload is handed to the module, which verifies it against the mirrored
 * verification state and queues it for the JS layer.
 */
class KeryxUnifiedPushService : PushService() {
  override fun onNewEndpoint(endpoint: PushEndpoint, instance: String) {
    KeryxPushModule.onNewEndpoint(
      applicationContext,
      endpoint.url,
      endpoint.pubKeySet?.pubKey ?: "",
      endpoint.pubKeySet?.auth ?: "",
    )
  }

  override fun onMessage(message: PushMessage, instance: String) {
    KeryxPushModule.onMessage(applicationContext, message.content)
  }

  override fun onRegistrationFailed(reason: FailedReason, instance: String) {
    KeryxPushModule.onRegistrationFailed(reason.name)
  }

  override fun onUnregistered(instance: String) {
    KeryxPushModule.onUnregistered(applicationContext)
  }

  override fun onTempUnavailable(instance: String) {
    KeryxPushModule.onTempUnavailable()
  }
}
