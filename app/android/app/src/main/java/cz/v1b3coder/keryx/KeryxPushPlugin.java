package cz.v1b3coder.keryx;

import android.content.Intent;
import android.content.pm.PackageManager;
import android.content.pm.ResolveInfo;
import android.os.Build;

import com.getcapacitor.JSArray;
import com.getcapacitor.JSObject;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;

import java.util.ArrayList;
import java.util.List;

/**
 * Push transport support for the web layer (design/notifications.md).
 *
 * The UnifiedPush probe is real: it lists the installed distributors exactly
 * like the UnifiedPush connector library does — broadcast receivers handling
 * org.unifiedpush.android.distributor.REGISTER, exported or in this app.
 *
 * MOCK (FCM phase): `fcm` is always false until the FCM phase lands, so the
 * web layer always takes the UnifiedPush (ntfy) branch.
 * TODO(fcm): report Google Play services availability (com.google.android.gms).
 */
@CapacitorPlugin(name = "KeryxPush")
public class KeryxPushPlugin extends Plugin {
    private static final String ACTION_REGISTER = "org.unifiedpush.android.distributor.REGISTER";

    @PluginMethod
    public void getSupport(PluginCall call) {
        JSObject ret = new JSObject();
        // MOCK: always "no Google services" until the FCM phase (TODO: FCM).
        ret.put("fcm", false);

        List<String> distributors = unifiedPushDistributors();
        JSArray list = new JSArray();
        for (String distributor : distributors) list.put(distributor);
        JSObject unifiedPush = new JSObject();
        unifiedPush.put("available", !distributors.isEmpty());
        unifiedPush.put("distributors", list);
        ret.put("unifiedPush", unifiedPush);

        call.resolve(ret);
    }

    /** Installed UnifiedPush distributors, like UnifiedPush.getDistributors(). */
    private List<String> unifiedPushDistributors() {
        Intent intent = new Intent(ACTION_REGISTER);
        List<ResolveInfo> resolved;
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            resolved = getContext().getPackageManager().queryBroadcastReceivers(
                    intent,
                    PackageManager.ResolveInfoFlags.of(
                            PackageManager.GET_META_DATA + PackageManager.GET_RESOLVED_FILTER));
        } else {
            resolved = getContext().getPackageManager().queryBroadcastReceivers(
                    intent, PackageManager.GET_RESOLVED_FILTER);
        }
        List<String> packages = new ArrayList<>();
        for (ResolveInfo info : resolved) {
            if (info.activityInfo == null) continue;
            boolean exported = info.activityInfo.exported;
            String name = info.activityInfo.packageName;
            if (!exported && !name.equals(getContext().getPackageName())) continue;
            if (!packages.contains(name)) packages.add(name);
        }
        return packages;
    }
}
