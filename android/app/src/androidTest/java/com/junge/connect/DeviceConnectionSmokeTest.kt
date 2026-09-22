package com.junge.connect

import android.util.Log
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import kotlinx.coroutines.runBlocking
import org.json.JSONObject
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assume
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Bounded real-device smoke test for the private device connection.
 *
 * Input is the one-shot, locally pushed app-private `qa-device-test.json`; it is
 * removed in `finally` before any native work, so the cleanup boundary holds no
 * matter how the device behaves. Only optional parameters are read from it: no
 * device id, token or pairing material is hard-coded here, and failure messages
 * never echo the payload. The test never raises the system VPN (that needs the
 * user's VPN consent), so `vpnRunning` is expected to stay false throughout; what
 * it pins is that stopping the VPN side must not drop the device connection.
 */
@RunWith(AndroidJUnit4::class)
class DeviceConnectionSmokeTest {
    @Test
    fun deviceConnectionSurvivesStoppingTheVpnSide() {
        val targetContext = InstrumentationRegistry.getInstrumentation().targetContext
        val repository = (targetContext.applicationContext as JunGoApplication).repository

        val input = File(targetContext.filesDir, QA_INPUT)
        Assume.assumeTrue("$QA_INPUT not present; skipping device connection smoke test", input.isFile)
        val params = try {
            JSONObject(input.readText(Charsets.UTF_8))
        } catch (t: Throwable) {
            // JSONObject exceptions can embed the raw payload; never surface them.
            throw AssertionError("$QA_INPUT could not be read as a JSON object (details redacted)")
        } finally {
            // One-shot input: always removed, success or failure, before any state work.
            input.delete()
        }

        val before = state(repository, "start")
        Assume.assumeTrue("Phone is not paired; skipping device connection smoke test", before.optBoolean("paired", false))

        // 1. The device connection runs with the system VPN off.
        call(repository, "device connection start") { repository.request("network", json("mesh" to true, "proxy" to false)) }
        val connected = state(repository, "device connection start")
        assertTrue("device connection is not running after native network(mesh=true, proxy=false)", connected.optBoolean("meshRunning", false))
        assertFalse("the device connection must not raise the system VPN", connected.optBoolean("vpnRunning", false))
        report("mesh-up", connected)

        // 2. Stopping the VPN side issues the same native call Repository.stopVpn
        //    makes for a TUN that goes away. The device connection must survive it.
        call(repository, "vpn stop") { repository.request("network", json("proxy" to false)) }
        val stopped = state(repository, "vpn stop")
        assertFalse("vpnRunning is true after stopping the VPN side", stopped.optBoolean("vpnRunning", false))
        assertTrue("the device connection was dropped when the VPN side stopped", stopped.optBoolean("meshRunning", false))
        report("vpn-stopped", stopped)

        // 3. Optional text, only when the input supplied it.
        val text = params.optString("text", "")
        if (text.isEmpty()) {
            println("DeviceConnectionSmoke text step skipped: no text parameter supplied")
            return
        }
        val deviceId = params.optString("deviceId", "").ifBlank { firstPeerId(stopped) }
        Assume.assumeTrue("no deviceId parameter or paired peer; skipping the optional text step", deviceId.isNotBlank())
        call(repository, "text send") { repository.request("chatSendText", json("deviceId" to deviceId, "text" to text)) }
        val after = state(repository, "text send")
        assertTrue("the supplied test text was not queued", queuedTextFor(repository, deviceId))
        report("text-sent", after)
    }

    private fun state(repository: NativeRepository, phase: String): JSONObject = try {
        runBlocking { repository.request("state") }
    } catch (t: Throwable) {
        throw AssertionError("state request failed after $phase (details redacted)")
    }

    private fun call(repository: NativeRepository, phase: String, block: suspend () -> Unit) {
        try {
            runBlocking { block() }
        } catch (t: Throwable) {
            throw AssertionError("native call failed during $phase (details redacted)")
        }
    }

    /** Chat text of the supplied peer only; the payload itself is never printed. */
    private fun queuedTextFor(repository: NativeRepository, deviceId: String): Boolean = try {
        runBlocking { repository.request("chatList") }.optJSONArray("messages")?.let { messages ->
            (0 until messages.length()).any { index ->
                val message = messages.optJSONObject(index) ?: return@any false
                message.optString("deviceId") == deviceId && message.optString("kind") == "text"
            }
        } ?: false
    } catch (t: Throwable) {
        throw AssertionError("chatList request failed (details redacted)")
    }

    private fun firstPeerId(state: JSONObject): String {
        val peers = state.optJSONArray("peers") ?: return ""
        for (index in 0 until peers.length()) {
            val id = peers.optJSONObject(index)?.optString("id").orEmpty()
            if (id.isNotBlank()) return id
        }
        return ""
    }

    private fun report(phase: String, state: JSONObject) {
        val line = "DeviceConnectionSmoke phase=$phase paired=${state.optBoolean("paired", false)}" +
            " meshRunning=${state.optBoolean("meshRunning", false)} vpnRunning=${state.optBoolean("vpnRunning", false)}"
        println(line)
        Log.i("DeviceConnectionSmoke", line)
    }

    private companion object {
        const val QA_INPUT = "qa-device-test.json"
    }
}
