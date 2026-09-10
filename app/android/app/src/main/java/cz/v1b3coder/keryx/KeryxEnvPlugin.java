package cz.v1b3coder.keryx;

import android.content.pm.ApplicationInfo;

import com.getcapacitor.JSObject;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;

/**
 * Exposes the build type to the web layer. The Keryx protocol is HTTPS-only;
 * the local-dev HTTP exception (plain-HTTP demo on a private network) is a
 * debug-build convenience and must never be active in release builds, so the
 * app's debuggable flag is what the JS layer keys on.
 */
@CapacitorPlugin(name = "KeryxEnv")
public class KeryxEnvPlugin extends Plugin {
    @PluginMethod
    public void isDebug(PluginCall call) {
        boolean debug = (getContext().getApplicationInfo().flags & ApplicationInfo.FLAG_DEBUGGABLE) != 0;
        JSObject ret = new JSObject();
        ret.put("debug", debug);
        call.resolve(ret);
    }
}
