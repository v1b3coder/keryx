package cz.v1b3coder.keryx.push

import android.content.Context
import com.google.android.gms.common.ConnectionResult
import com.google.android.gms.common.GoogleApiAvailability
import com.google.android.gms.tasks.Tasks
import com.google.firebase.messaging.FirebaseMessaging
import expo.modules.kotlin.Promise
import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition
import org.json.JSONObject
import org.unifiedpush.android.connector.UnifiedPush
import java.nio.charset.StandardCharsets

/**
 * Wake-up transport support for the JS layer (design/notifications.md).
 *
 * The UnifiedPush probe and the connector registration are real. The connector
 * library receives and decrypts the RFC 8291 message and hands the plaintext §4
 * envelope to [KeryxUnifiedPushService]; the module verifies it natively
 * against the mirrored verification state, queues it for the JS layer, acks the
 * relay's liveness heartbeat and shows the generic notice when no page is
 * listening.
 *
 * The FCM probe is the real Google Play services availability check, and
 * `setTopics` subscribes the Firebase SDK to exactly the union of every followed
 * company's topics (relay/SPECIFICATION.md §6.1). The topic leg is registry-free
 * and anonymous: the relay never learns the device's FCM token.
 */
class KeryxPushModule : Module() {
  private val context: Context
    get() = appContext.reactContext ?: throw IllegalStateException("React context lost")

  private val pendingRegistrations = mutableListOf<Promise>()
  private val lock = Any()

  private var hasPushListeners = false

  override fun definition() = ModuleDefinition {
    Name("KeryxPush")
    Events("push")

    OnCreate {
      KeryxPushState.instance = this@KeryxPushModule
    }
    OnDestroy {
      if (KeryxPushState.instance === this@KeryxPushModule) KeryxPushState.instance = null
    }
    OnStartObserving {
      hasPushListeners = true
    }
    OnStopObserving {
      hasPushListeners = false
    }

    // --- transport probe -----------------------------------------------------

    AsyncFunction("getSupport") {
      val fcm = GoogleApiAvailability.getInstance()
        .isGooglePlayServicesAvailable(context) == ConnectionResult.SUCCESS
      val distributors = UnifiedPush.getDistributors(context)
      mapOf(
        "fcm" to fcm,
        "unifiedPush" to mapOf(
          "available" to distributors.isNotEmpty(),
          "distributors" to distributors,
        ),
      )
    }

    // --- connector registration (§5.3, §6.3) ---------------------------------

    AsyncFunction("register") { options: Map<String, Any?>, promise: Promise ->
      val vapid = options["vapid"] as? String ?: ""
      if (KeryxPushState.prefs(context).getString("endpoint", null) != null) {
        // The endpoint is stable across restarts: resolve now and refresh the
        // distributor registration in the background (UnifiedPush: register on
        // every application start).
        promise.resolve(endpointResult())
        registerWithDistributor(vapid)
        return@AsyncFunction
      }
      synchronized(lock) { pendingRegistrations.add(promise) }
      val activity = appContext.activityProvider?.currentActivity
      if (activity == null) {
        rejectPending("no activity")
        return@AsyncFunction
      }
      activity.runOnUiThread {
        UnifiedPush.tryUseCurrentOrDefaultDistributor(activity) { success ->
          if (!success) {
            rejectPending("no UnifiedPush distributor")
          } else {
            registerWithDistributor(vapid)
          }
        }
      }
    }

    AsyncFunction("unregister") {
      UnifiedPush.unregister(context, KeryxPushState.INSTANCE)
    }

    AsyncFunction("getEndpoint") {
      val p = KeryxPushState.prefs(context)
      mapOf(
        "endpoint" to p.getString("endpoint", null),
        "p256dh" to p.getString("p256dh", null),
        "auth" to p.getString("auth", null),
      )
    }

    // --- FCM topic leg (relay/SPECIFICATION.md §6.1) -------------------------

    AsyncFunction("setTopics") { options: Map<String, Any?> ->
      val topics = (options["topics"] as? List<*>)?.filterIsInstance<String>() ?: emptyList()
      val wanted = topics.filter { it.isNotEmpty() }.toMutableSet()
      KeryxPushState.execute {
        // The diff is computed inside the executor, which serializes the whole
        // read-diff-apply-write cycle: a diff computed by a caller that raced
        // an earlier setTopics can never be applied to the newer SDK state. The
        // applied set is persisted after every operation, so a failed cycle
        // leaves the mirror at what the SDK really follows.
        val applied = KeryxPushState.subscribedTopics(context)
        val added = KeryxTopics.added(applied, wanted)
        val removed = KeryxTopics.removed(applied, wanted)
        try {
          val messaging = FirebaseMessaging.getInstance()
          for (topic in added) {
            Tasks.await(messaging.subscribeToTopic(topic))
            applied.add(topic)
            KeryxPushState.putSubscribedTopics(context, applied)
          }
          for (topic in removed) {
            Tasks.await(messaging.unsubscribeFromTopic(topic))
            applied.remove(topic)
            KeryxPushState.putSubscribedTopics(context, applied)
          }
        } catch (e: Exception) {
          throw IllegalStateException("FCM topic subscription failed: ${e.message}")
        }
        mapOf("topics" to wanted.toList())
      }
    }

    AsyncFunction("getTopics") {
      mapOf("topics" to KeryxPushState.subscribedTopics(context).toList())
    }

    // --- the native verification mirror and the ack credentials ----------------

    AsyncFunction("setVerifyState") { options: Map<String, Any?> ->
      @Suppress("UNCHECKED_CAST")
      val state = options["state"] as? Map<String, Any?> ?: emptyMap()
      KeryxPushState.putVerifyState(context, JSONObject(state))
    }

    AsyncFunction("setRegistration") { options: Map<String, Any?> ->
      @Suppress("UNCHECKED_CAST")
      val registration = options["registration"] as? Map<String, Any?>
      KeryxPushState.putRegistration(context, registration?.let { JSONObject(it) })
    }

    // --- notifications ------------------------------------------------------

    AsyncFunction("showNotification") { options: Map<String, Any?> ->
      val title = options["title"] as? String ?: "Keryx"
      val body = options["body"] as? String ?: "New update available"
      val tag = options["tag"] as? String ?: "keryx-wakeup"
      KeryxPushState.postNotification(context, title, body, tag)
    }

    AsyncFunction("drainMessages") {
      mapOf("messages" to KeryxPushState.drainQueue(context))
    }
  }

  private fun endpointResult(): Map<String, Any?> {
    val p = KeryxPushState.prefs(context)
    return mapOf(
      "endpoint" to p.getString("endpoint", null),
      "p256dh" to p.getString("p256dh", null),
      "auth" to p.getString("auth", null),
    )
  }

  private fun registerWithDistributor(vapid: String) {
    try {
      UnifiedPush.register(context, KeryxPushState.INSTANCE, "Keryx notifications", vapid)
    } catch (e: Exception) {
      rejectPending("UnifiedPush registration failed: ${e.message}")
    }
  }

  private fun rejectPending(message: String) {
    val pending: List<Promise>
    synchronized(lock) {
      pending = pendingRegistrations.toList()
      pendingRegistrations.clear()
    }
    for (call in pending) call.reject("ERR_KERYX_PUSH", message, null)
  }

  /** The connector service callbacks (KeryxUnifiedPushService). */
  companion object {
    @JvmStatic
    fun onNewEndpoint(context: Context, url: String, p256dh: String, auth: String) {
      KeryxPushState.putEndpoint(context, url, p256dh, auth)
      val instance = KeryxPushState.instance ?: return
      instance.resolvePendingEndpoints()
    }

    @JvmStatic
    fun onRegistrationFailed(message: String) {
      KeryxPushState.instance?.rejectPendingRegistrations("UnifiedPush registration failed: $message")
    }

    @JvmStatic
    fun onUnregistered(context: Context) {
      KeryxPushState.clearEndpoint(context)
    }

    @JvmStatic
    fun onTempUnavailable() {
      // the distributor is temporarily unreachable; polling remains the backstop
    }

    /**
     * One wake-up from the distributor or the FCM handler. No TUF metadata or
     * content work: the envelope is verified against the mirrored state, queued
     * for the page (verified or not, so a stale mirror costs a delayed notice,
     * never a lost wake-up), and — when accepted — acked once and announced
     * natively unless a JS listener is attached (then the page owns
     * verification, recovery and sync).
     */
    @JvmStatic
    fun onMessage(context: Context, content: ByteArray) {
      val payload = String(content, StandardCharsets.UTF_8)
      val verify = WakeupVerify()
      try {
        verify.setState(KeryxPushState.verifyState(context))
      } catch (ignored: Exception) {
        // a corrupt mirror verifies nothing: the wake-up is queued for the page
      }
      val state = verify.verify(payload)
      if (state != null) {
        KeryxPushState.putVerifyState(context, verify.toJson())
      }
      KeryxPushState.enqueue(context, payload)
      val instance = KeryxPushState.instance
      if (state == null) {
        // queued for the page; never acked or announced natively
        if (instance != null) instance.emitPush(payload)
        return
      }
      KeryxPushState.ackReceipt(context)
      // the locally verified channel label: the generic notice names it, the
      // tag keys the notice off the wake-up's own topic
      val body = if (state.label.isEmpty()) "New update available" else "New update in ${state.label}"
      val tag = if (state.topic.isEmpty()) "keryx-wakeup" else "keryx-${state.topic}"
      if (instance != null && instance.emitPush(payload)) {
        // the page is alive and owns verification, recovery and sync
      } else {
        KeryxPushState.postNotification(context, "Keryx", body, tag)
      }
    }
  }

  private fun resolvePendingEndpoints() {
    val pending: List<Promise>
    synchronized(lock) {
      pending = pendingRegistrations.toList()
      pendingRegistrations.clear()
    }
    for (call in pending) call.resolve(endpointResult())
  }

  private fun rejectPendingRegistrations(message: String) {
    val pending: List<Promise>
    synchronized(lock) {
      pending = pendingRegistrations.toList()
      pendingRegistrations.clear()
    }
    for (call in pending) call.reject("ERR_KERYX_PUSH", message, null)
  }

  /** Emit one verified payload to the page; false when no listener is attached. */
  fun emitPush(payload: String): Boolean {
    if (!hasPushListeners) return false
    val activity = appContext.activityProvider?.currentActivity ?: return false
    activity.runOnUiThread {
      try {
        sendEvent("push", mapOf("payload" to payload))
      } catch (e: Exception) {
        // the JS runtime went away mid-delivery: the queued copy remains
      }
    }
    return true
  }
}
