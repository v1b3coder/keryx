package cz.v1b3coder.keryx;

import android.content.pm.ApplicationInfo;
import android.os.Bundle;
import android.webkit.WebSettings;

import com.getcapacitor.BridgeActivity;

public class MainActivity extends BridgeActivity {
    @Override
    public void onCreate(Bundle savedInstanceState) {
        registerPlugin(KeryxEnvPlugin.class);
        super.onCreate(savedInstanceState);

        // Local-dev HTTP exception, debug builds only: the WebView page is
        // served at https://localhost, so http:// fetches (the plain-HTTP
        // demo on a private network) are otherwise blocked as mixed content.
        // Release builds keep the WebView default (mixed content blocked) on
        // top of the strict network security config (no cleartext).
        boolean debug = (getApplicationInfo().flags & ApplicationInfo.FLAG_DEBUGGABLE) != 0;
        if (debug) {
            getBridge()
                    .getWebView()
                    .getSettings()
                    .setMixedContentMode(WebSettings.MIXED_CONTENT_ALWAYS_ALLOW);
        }
    }
}
