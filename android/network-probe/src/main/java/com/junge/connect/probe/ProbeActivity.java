package com.junge.connect.probe;

import android.app.Activity;
import android.os.Bundle;
import android.os.Process;
import android.util.Log;
import android.widget.TextView;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;

/** A separate-UID, actual Android network-stack probe. Never part of the app APK. */
public final class ProbeActivity extends Activity {
    private TextView result;
    @Override public void onCreate(Bundle state) {
        super.onCreate(state);
        result = new TextView(this);
        result.setPadding(40, 100, 40, 40);
        result.setTextSize(20);
        setContentView(result);
        if (getIntent().getBooleanExtra("share", false)) {
            try {
                java.io.File file = new java.io.File(getFilesDir(), "share-proof.txt");
                try (java.io.FileOutputStream output = new java.io.FileOutputStream(file)) { output.write("JunGo ephemeral ACTION_SEND QA 20260922\n".getBytes(StandardCharsets.UTF_8)); }
                android.net.Uri uri = android.net.Uri.parse("content://com.junge.connect.probe.share/share-proof.txt");
                android.content.Intent send = new android.content.Intent(android.content.Intent.ACTION_SEND)
                    .setType("text/plain").setComponent(new android.content.ComponentName("com.junge.connect", "com.junge.connect.MainActivity"))
                    .putExtra(android.content.Intent.EXTRA_STREAM, uri).putExtra(android.content.Intent.EXTRA_TEXT, "QA external share")
                    .addFlags(android.content.Intent.FLAG_GRANT_READ_URI_PERMISSION);
                send.setClipData(android.content.ClipData.newRawUri("QA source", uri));
                startActivity(send); result.setText("Temporary content URI shared for QA.");
            } catch (Exception e) { result.setText("Share QA failed: " + e); }
            return;
        }
        String target = getIntent().getStringExtra("url");
        if (target == null) { result.setText("QA 网络探针\n通过测试 Intent 提供 url。\nUID " + Process.myUid()); return; }
        result.setText("UID " + Process.myUid() + "\n请求 " + target + " …");
        new Thread(() -> probe(target), "jungo-network-probe").start();
    }
    private void probe(String target) {
        HttpURLConnection connection = null;
        String report;
        long started = System.nanoTime();
        try {
            URL url = new URL(target);
            if (!url.getProtocol().equals("http") && !url.getProtocol().equals("https")) throw new IllegalArgumentException("HTTP(S) only");
            connection = (HttpURLConnection) url.openConnection();
            connection.setConnectTimeout(12000);
            connection.setReadTimeout(12000);
            connection.setInstanceFollowRedirects(false);
            connection.setRequestProperty("X-JunGo-Probe-UID", String.valueOf(Process.myUid()));
            int code = connection.getResponseCode();
            byte[] buffer = new byte[4096];
            int read;
            try (InputStream input = code < 400 ? connection.getInputStream() : connection.getErrorStream()) { read = input == null ? 0 : input.read(buffer); }
            String body = new String(buffer, 0, Math.max(0, read), StandardCharsets.UTF_8);
            report = "UID " + Process.myUid() + "\nHTTP " + code + "\n" + target + "\n" + body + "\n" + ((System.nanoTime() - started) / 1000000) + " ms";
        } catch (Exception e) { report = "UID " + Process.myUid() + "\nFAIL " + target + "\n" + e.getClass().getSimpleName() + ": " + e.getMessage(); }
        finally { if (connection != null) connection.disconnect(); }
        String finalReport = report;
        Log.i("JunGoProbe", report.replace('\n', ' '));
        runOnUiThread(() -> result.setText(finalReport));
    }
}
