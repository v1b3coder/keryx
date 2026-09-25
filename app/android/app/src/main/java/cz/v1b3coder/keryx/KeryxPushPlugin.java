package cz.v1b3coder.keryx;

import android.Manifest;
import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.os.Build;

import com.getcapacitor.JSArray;
import com.getcapacitor.JSObject;
import com.getcapacitor.PermissionState;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;
import com.getcapacitor.annotation.Permission;
import com.getcapacitor.annotation.PermissionCallback;

import org.json.JSONArray;
import org.json.JSONObject;
import org.unifiedpush.android.connector.UnifiedPush;

import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/**
 * Wake-up transport support for the web layer (design/notifications.md).
 *
 * The UnifiedPush probe and the connector registration are real. The connector
 * library receives and decrypts the RFC 8291 message and hands the plaintext §4
 * envelope to {@link KeryxPushService}; the plugin verifies it natively against
 * the mirrored verification state, queues it for the JS layer, acks the relay's
 * liveness heartbeat and shows the generic notice when no page is listening.
 *
 * MOCK (FCM phase): `fcm` is always false until the FCM phase lands, so the web
 * layer always takes the UnifiedPush (ntfy) branch.
 * TODO(fcm): report Google Play services availability
 * (GoogleApiAvailability.isGooglePlayServicesAvailable == SUCCESS).
 */
@CapacitorPlugin(
        name = "KeryxPush",
        permissions = { @Permission(alias = "notifications", strings = { Manifest.permission.POST_NOTIFICATIONS }) })
public class KeryxPushPlugin extends Plugin {
    static final String PREFS = "keryx_push";
    static final String INSTANCE = "default";
    static final String CHANNEL_ID = "keryx-wakeup";
    private static final boolean FCM_MOCK = false;
    private static final int MAX_QUEUE = 8;
    private static final String ENDPOINT_KEY = "endpoint";
    private static final String P256DH_KEY = "p256dh";
    private static final String AUTH_KEY = "auth";
    private static final String VERIFY_KEY = "verifyState";
    private static final String REGISTRATION_KEY = "registration";
    private static final String QUEUE_KEY = "queue";

    private static KeryxPushPlugin instance;
    private static PluginCall pendingRegistration;
    private static final Object lock = new Object();
    private static final ExecutorService io = Executors.newSingleThreadExecutor();

    @Override
    public void load() {
        instance = this;
    }

    @Override
    protected void handleOnDestroy() {
        synchronized (lock) {
            if (instance == this) instance = null;
        }
    }

    // --- transport probe -----------------------------------------------------

    @PluginMethod
    public void getSupport(PluginCall call) {
        JSObject ret = new JSObject();
        // MOCK (FCM phase): no Google services until the FCM phase.
        ret.put("fcm", FCM_MOCK);
        List<String> distributors = UnifiedPush.getDistributors(getContext());
        JSArray list = new JSArray();
        for (String distributor : distributors) list.put(distributor);
        JSObject unifiedPush = new JSObject();
        unifiedPush.put("available", !distributors.isEmpty());
        unifiedPush.put("distributors", list);
        ret.put("unifiedPush", unifiedPush);
        call.resolve(ret);
    }

    // --- connector registration (§5.3, §6.3) ---------------------------------

    @PluginMethod
    public void register(PluginCall call) {
        final String vapid = call.getString("vapid", "");
        final Context context = getContext();
        if (prefs(context).getString(ENDPOINT_KEY, null) != null) {
            // The endpoint is stable across restarts: resolve now and refresh the
            // distributor registration in the background (UnifiedPush: register on
            // every application start).
            call.resolve(endpointResult(context));
            registerWithDistributor(context, vapid);
            return;
        }
        synchronized (lock) {
            pendingRegistration = call;
        }
        call.setKeepAlive(true);
        getActivity().runOnUiThread(() -> UnifiedPush.tryUseCurrentOrDefaultDistributor(getActivity(), success -> {
            if (!success) {
                rejectPending("no UnifiedPush distributor");
                return kotlin.Unit.INSTANCE;
            }
            registerWithDistributor(context, vapid);
            return kotlin.Unit.INSTANCE;
        }));
    }

    private static void registerWithDistributor(Context context, String vapid) {
        try {
            UnifiedPush.register(context, INSTANCE, "Keryx notifications", vapid);
        } catch (Exception e) {
            rejectPending("UnifiedPush registration failed: " + e.getMessage());
        }
    }

    @PluginMethod
    public void unregister(PluginCall call) {
        UnifiedPush.unregister(getContext(), INSTANCE);
        call.resolve();
    }

    @PluginMethod
    public void getEndpoint(PluginCall call) {
        SharedPreferences p = prefs(getContext());
        JSObject ret = new JSObject();
        ret.put("endpoint", p.getString(ENDPOINT_KEY, null));
        ret.put("p256dh", p.getString(P256DH_KEY, null));
        ret.put("auth", p.getString(AUTH_KEY, null));
        call.resolve(ret);
    }

    // --- the native verification mirror and the ack credentials ----------------

    @PluginMethod
    public void setVerifyState(PluginCall call) {
        JSONObject state = call.getObject("state");
        prefs(getContext()).edit().putString(VERIFY_KEY, state == null ? "{}" : state.toString()).apply();
        call.resolve();
    }

    @PluginMethod
    public void setRegistration(PluginCall call) {
        // the credentials the worker needs for its liveness/delivery ack (§5.3);
        // null clears them
        JSObject registration = call.getObject("registration");
        SharedPreferences.Editor editor = prefs(getContext()).edit();
        if (registration == null) editor.remove(REGISTRATION_KEY);
        else editor.putString(REGISTRATION_KEY, registration.toString());
        editor.apply();
        call.resolve();
    }

    // --- notifications -------------------------------------------------------

    @PluginMethod
    public void showNotification(PluginCall call) {
        postNotification(
                getContext(),
                call.getString("title", "Keryx"),
                call.getString("body", "New update available"));
        call.resolve();
    }

    @PluginMethod
    public void requestNotificationPermission(PluginCall call) {
        if (Build.VERSION.SDK_INT < 33 || getPermissionState("notifications") == PermissionState.GRANTED) {
            resolveNotificationPermission(call);
            return;
        }
        requestPermissionForAlias("notifications", call, "notificationPermissionCallback");
    }

    @PermissionCallback
    private void notificationPermissionCallback(PluginCall call) {
        resolveNotificationPermission(call);
    }

    private void resolveNotificationPermission(PluginCall call) {
        JSObject ret = new JSObject();
        ret.put("granted", Build.VERSION.SDK_INT < 33
                || getPermissionState("notifications") == PermissionState.GRANTED);
        call.resolve(ret);
    }

    @PluginMethod
    public void getNotificationPermission(PluginCall call) {
        resolveNotificationPermission(call);
    }

    @PluginMethod
    public void drainMessages(PluginCall call) {
        JSArray messages = new JSArray();
        for (String payload : drainQueue(getContext())) messages.put(payload);
        JSObject ret = new JSObject();
        ret.put("messages", messages);
        call.resolve(ret);
    }

    // --- the connector service callbacks (KeryxPushService) --------------------

    static void onNewEndpoint(Context context, String url, String p256dh, String auth) {
        prefs(context)
                .edit()
                .putString(ENDPOINT_KEY, url)
                .putString(P256DH_KEY, p256dh)
                .putString(AUTH_KEY, auth)
                .apply();
        PluginCall pending;
        synchronized (lock) {
            pending = pendingRegistration;
            pendingRegistration = null;
        }
        if (pending != null) pending.resolve(endpointResult(context));
    }

    static void onRegistrationFailed(String reason) {
        rejectPending("UnifiedPush registration failed: " + reason);
    }

    static void onUnregistered(Context context) {
        prefs(context).edit().remove(ENDPOINT_KEY).remove(P256DH_KEY).remove(AUTH_KEY).apply();
    }

    static void onTempUnavailable() {
        // the distributor is temporarily unreachable; polling remains the backstop
    }

    /**
     * One wake-up from the distributor. No TUF metadata or content work: the
     * envelope is verified against the mirrored state, queued for the page (verified
     * or not, so a stale mirror costs a delayed notice, never a lost wake-up),
     * and — when accepted — acked once and announced natively unless a JS listener
     * is attached (then the page owns verification, recovery and sync).
     */
    static void onMessage(Context context, byte[] content) {
        String payload = new String(content, StandardCharsets.UTF_8);
        WakeupVerify verify = new WakeupVerify();
        try {
            verify.setState(new JSONObject(prefs(context).getString(VERIFY_KEY, "{}")));
        } catch (Exception ignored) {
            // a corrupt mirror verifies nothing: the wake-up is queued for the page
        }
        boolean accepted = verify.verify(payload);
        if (accepted) {
            prefs(context).edit().putString(VERIFY_KEY, verify.toJson().toString()).apply();
        }
        enqueue(context, payload);
        if (!accepted) return; // queued for the page; never ack or notify
        ackReceipt(context);
        final KeryxPushPlugin self = instance;
        if (self != null && self.getActivity() != null) {
            self.getActivity().runOnUiThread(() -> {
                if (self.hasListeners("push")) {
                    JSObject data = new JSObject();
                    data.put("payload", payload);
                    self.notifyListeners("push", data, true);
                } else {
                    postNotification(context, "Keryx", "New update available");
                }
            });
        } else {
            postNotification(context, "Keryx", "New update available");
        }
    }

    // --- helpers -------------------------------------------------------------

    private static SharedPreferences prefs(Context context) {
        return context.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
    }

    private static JSObject endpointResult(Context context) {
        SharedPreferences p = prefs(context);
        JSObject ret = new JSObject();
        ret.put("endpoint", p.getString(ENDPOINT_KEY, null));
        ret.put("p256dh", p.getString(P256DH_KEY, null));
        ret.put("auth", p.getString(AUTH_KEY, null));
        return ret;
    }

    private static void rejectPending(String message) {
        PluginCall pending;
        synchronized (lock) {
            pending = pendingRegistration;
            pendingRegistration = null;
        }
        if (pending != null) pending.reject(message);
    }

    private static void enqueue(Context context, String payload) {
        synchronized (lock) {
            JSONArray queue = loadQueue(context);
            queue.put(payload);
            while (queue.length() > MAX_QUEUE) queue.remove(0);
            prefs(context).edit().putString(QUEUE_KEY, queue.toString()).apply();
        }
    }

    private static List<String> drainQueue(Context context) {
        List<String> out = new ArrayList<>();
        synchronized (lock) {
            JSONArray queue = loadQueue(context);
            for (int i = 0; i < queue.length(); i++) {
                String payload = queue.optString(i, null);
                if (payload != null) out.add(payload);
            }
            prefs(context).edit().remove(QUEUE_KEY).apply();
        }
        return out;
    }

    private static JSONArray loadQueue(Context context) {
        String raw = prefs(context).getString(QUEUE_KEY, null);
        if (raw == null) return new JSONArray();
        try {
            return new JSONArray(raw);
        } catch (Exception e) {
            return new JSONArray();
        }
    }

    /**
     * The worker's liveness/delivery ack (§5.3: "Sent by the service worker on
     * wake-up receipt"): one POST, no recovery. A gone registration is left to
     * the page's foreground check, which owns recovery. Failures are swallowed.
     */
    private static void ackReceipt(Context context) {
        String regJson = prefs(context).getString(REGISTRATION_KEY, null);
        if (regJson == null) return;
        String baseUrl;
        String id;
        String token;
        try {
            JSONObject reg = new JSONObject(regJson);
            baseUrl = reg.optString("baseUrl", "");
            id = reg.optString("id", "");
            token = reg.optString("managementToken", "");
        } catch (Exception e) {
            return;
        }
        if (baseUrl.isEmpty() || id.isEmpty() || token.isEmpty()) return;
        io.execute(() -> {
            HttpURLConnection conn = null;
            try {
                conn = (HttpURLConnection) new URL(baseUrl + "/v1/registrations/" + id + "/heartbeat").openConnection();
                conn.setRequestMethod("POST");
                conn.setRequestProperty("Authorization", "Bearer " + token);
                conn.setConnectTimeout(15_000);
                conn.setReadTimeout(15_000);
                conn.getResponseCode();
            } catch (Exception ignored) {
                // best-effort: the page's foreground check recovers a gone registration
            } finally {
                if (conn != null) conn.disconnect();
            }
        });
    }

    /** The generic native notice; it never carries content. */
    private static void postNotification(Context context, String title, String body) {
        NotificationManager nm = (NotificationManager) context.getSystemService(Context.NOTIFICATION_SERVICE);
        if (nm == null) return;
        if (Build.VERSION.SDK_INT >= 26) {
            NotificationChannel channel = new NotificationChannel(
                    CHANNEL_ID, "Keryx updates", NotificationManager.IMPORTANCE_DEFAULT);
            nm.createNotificationChannel(channel);
        }
        Intent intent = new Intent(context, MainActivity.class);
        intent.setFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP | Intent.FLAG_ACTIVITY_NEW_TASK);
        PendingIntent pi = PendingIntent.getActivity(
                context, 0, intent, PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        Notification.Builder builder = Build.VERSION.SDK_INT >= 26
                ? new Notification.Builder(context, CHANNEL_ID)
                : new Notification.Builder(context);
        builder.setSmallIcon(android.R.drawable.ic_dialog_info)
                .setContentTitle(title)
                .setContentText(body)
                .setAutoCancel(true)
                .setContentIntent(pi);
        nm.notify("keryx-wakeup", 1, builder.build());
    }
}
