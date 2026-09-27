package cz.v1b3coder.keryx.push

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.SharedPreferences
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import org.json.JSONArray
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.Executors

/**
 * The native wake-up state shared by the Expo module and the two services
 * (design/notifications.md): the verification mirror (pushed by the JS layer
 * from its TUF-verified state), the queued payloads, the relay ack credentials
 * and the generic notice. The worker fetches no TUF metadata and no content.
 */
internal object KeryxPushState {
  const val PREFS = "keryx_push"
  const val INSTANCE = "default"
  const val CHANNEL_ID = "keryx-wakeup"

  private const val MAX_QUEUE = 8
  private const val ENDPOINT_KEY = "endpoint"
  private const val P256DH_KEY = "p256dh"
  private const val AUTH_KEY = "auth"
  private const val VERIFY_KEY = "verifyState"
  private const val REGISTRATION_KEY = "registration"
  private const val QUEUE_KEY = "queue"
  private const val TOPICS_KEY = "topics"

  private val lock = Any()
  private val io = Executors.newSingleThreadExecutor()

  /** The module instance, for `sendEvent` while the JS runtime is alive. */
  @Volatile
  var instance: KeryxPushModule? = null

  fun prefs(context: Context): SharedPreferences =
    context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

  /**
   * Run a block on the serialized IO executor and wait for its result: the
   * `setTopics` read-diff-apply-write cycle must not interleave with another.
   */
  fun <T> execute(block: () -> T): T {
    return io.submit(block).get()
  }

  /**
   * Hand a block to the serialized IO executor without waiting. Background work
   * (the heartbeat ack) must never block the FCM/UnifiedPush callback thread,
   * which is shared and single-threaded per transport: a slow POST there would
   * delay the notice and every following wake-up.
   */
  fun executeAsync(block: () -> Unit) {
    io.execute(block)
  }

  // --- queue -----------------------------------------------------------------

  fun enqueue(context: Context, payload: String) {
    synchronized(lock) {
      val queue = loadQueue(context)
      queue.put(payload)
      while (queue.length() > MAX_QUEUE) queue.remove(0)
      prefs(context).edit().putString(QUEUE_KEY, queue.toString()).apply()
    }
  }

  fun drainQueue(context: Context): List<String> {
    val out = mutableListOf<String>()
    synchronized(lock) {
      val queue = loadQueue(context)
      for (i in 0 until queue.length()) {
        queue.optString(i, null)?.let { out.add(it) }
      }
      prefs(context).edit().remove(QUEUE_KEY).apply()
    }
    return out
  }

  private fun loadQueue(context: Context): JSONArray {
    val raw = prefs(context).getString(QUEUE_KEY, null) ?: return JSONArray()
    return try {
      JSONArray(raw)
    } catch (e: Exception) {
      JSONArray()
    }
  }

  // --- FCM topics ------------------------------------------------------------

  fun subscribedTopics(context: Context): MutableSet<String> {
    val topics = mutableSetOf<String>()
    val raw = prefs(context).getString(TOPICS_KEY, "") ?: ""
    for (topic in raw.split("\n")) if (topic.isNotEmpty()) topics.add(topic)
    return topics
  }

  fun putSubscribedTopics(context: Context, topics: Set<String>) {
    prefs(context).edit().putString(TOPICS_KEY, topics.joinToString("\n")).apply()
  }

  // --- the connector registration and the ack credentials -----------------------

  fun putEndpoint(context: Context, url: String, p256dh: String, auth: String) {
    prefs(context).edit()
      .putString(ENDPOINT_KEY, url)
      .putString(P256DH_KEY, p256dh)
      .putString(AUTH_KEY, auth)
      .apply()
  }

  fun clearEndpoint(context: Context) {
    prefs(context).edit().remove(ENDPOINT_KEY).remove(P256DH_KEY).remove(AUTH_KEY).apply()
  }

  fun putVerifyState(context: Context, state: JSONObject) {
    prefs(context).edit().putString(VERIFY_KEY, state.toString()).apply()
  }

  fun verifyState(context: Context): JSONObject? {
    return try {
      JSONObject(prefs(context).getString(VERIFY_KEY, "{}"))
    } catch (e: Exception) {
      null
    }
  }

  fun putRegistration(context: Context, registration: JSONObject?) {
    val editor = prefs(context).edit()
    if (registration == null) editor.remove(REGISTRATION_KEY)
    else editor.putString(REGISTRATION_KEY, registration.toString())
    editor.apply()
  }

  // --- the liveness/delivery ack (§5.3) -------------------------------------

  /**
   * The worker's liveness/delivery ack: one POST, no recovery. A gone
   * registration is left to the page's foreground check, which owns recovery.
   */
  fun ackReceipt(context: Context) {
    val regJson = prefs(context).getString(REGISTRATION_KEY, null) ?: return
    val baseUrl: String
    val id: String
    val token: String
    try {
      val reg = JSONObject(regJson)
      baseUrl = reg.optString("baseUrl", "")
      id = reg.optString("id", "")
      token = reg.optString("managementToken", "")
    } catch (e: Exception) {
      return
    }
    if (baseUrl.isEmpty() || id.isEmpty() || token.isEmpty()) return
    executeAsync {
      var conn: HttpURLConnection? = null
      try {
        conn = URL("$baseUrl/v1/registrations/$id/heartbeat").openConnection() as HttpURLConnection
        conn.requestMethod = "POST"
        conn.setRequestProperty("Authorization", "Bearer $token")
        conn.connectTimeout = 15_000
        conn.readTimeout = 15_000
        conn.responseCode
      } catch (ignored: Exception) {
        // best-effort: the page's foreground check recovers a gone registration
      } finally {
        conn?.disconnect()
      }
    }
  }

  // --- the generic native notice ----------------------------------------------

  /**
   * The generic native notice; it never carries content. The tag is the
   * wake-up's own topic, so one channel's notice never replaces another's.
   */
  fun postNotification(context: Context, title: String, body: String, tag: String) {
    val nm = context.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
    if (Build.VERSION.SDK_INT >= 26) {
      val channel = NotificationChannel(CHANNEL_ID, "Keryx updates", NotificationManager.IMPORTANCE_DEFAULT)
      nm.createNotificationChannel(channel)
    }
    val launch = context.packageManager.getLaunchIntentForPackage(context.packageName)
    val pending = PendingIntent.getActivity(
      context,
      0,
      launch,
      PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )
    val builder = if (Build.VERSION.SDK_INT >= 26) {
      NotificationCompat.Builder(context, CHANNEL_ID)
    } else {
      NotificationCompat.Builder(context)
    }
    builder.setSmallIcon(android.R.drawable.ic_dialog_info)
      .setContentTitle(title)
      .setContentText(body)
      .setAutoCancel(true)
      .setContentIntent(pending)
    NotificationManagerCompat.from(context).notify(tag, 1, builder.build())
  }
}
