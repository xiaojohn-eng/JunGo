package com.junge.connect

import android.util.Log
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import kotlinx.coroutines.runBlocking
import org.json.JSONObject
import org.junit.Assert.assertTrue
import org.junit.Assume
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Bounded real-device pairing smoke test.
 *
 * The one-shot, locally pushed `qa-pairing-input.json` is consumed and removed first, so
 * the file-cleanup boundary holds no matter what the device state turns out to be. Only
 * then is a device that already reports `paired` skipped without any enrollment attempt,
 * so a provisioned phone is never re-paired. A device that is not yet paired feeds the
 * same params into the production `NativeRepository.request("pair")` entry point and the
 * native state is verified. It never starts activities, clears app data, installs
 * anything, or starts a VPN. Secrets are neither printed nor attached to failure messages.
 */
@RunWith(AndroidJUnit4::class)
class DevicePairingSmokeTest {
    @Test
    fun pairThroughNativeRepositoryAndVerifyState() {
        val targetContext = InstrumentationRegistry.getInstrumentation().targetContext
        val repository = (targetContext.applicationContext as JunGoApplication).repository

        val input = File(targetContext.filesDir, "qa-pairing-input.json")
        Assume.assumeTrue("qa-pairing-input.json not present; skipping pairing smoke test", input.isFile)

        val params = try {
            JSONObject(input.readText(Charsets.UTF_8))
        } catch (t: Throwable) {
            // JSONObject exceptions can embed the raw payload; never surface them.
            throw AssertionError("qa-pairing-input.json could not be read as a JSON object (details redacted)")
        } finally {
            // One-shot input: always remove it, success or failure, before any state work.
            input.delete()
        }

        // Never enroll or re-pair an already provisioned device; skip instead. This runs
        // after the one-shot input has been removed so the cleanup boundary cannot be
        // skipped by the assume.
        val state = requestState(repository)
        Assume.assumeFalse("Already paired; no enrollment attempted", state.optBoolean("paired", false))

        // Minimal input contract: an https control server and a 64-hex TLS fingerprint.
        val server = params.optString("server", "").trim()
        assertTrue("pairing input server must start with https://", server.startsWith("https://"))
        val fingerprint = params.optString("fingerprint", "").trim()
        assertTrue("pairing input fingerprint must be 64 hex characters", HEX64.matches(fingerprint))

        try {
            runBlocking { repository.request("pair", params) }
        } catch (t: Throwable) {
            // Pair errors may echo parameters; keep the failure opaque.
            throw AssertionError("pair request failed (details redacted)")
        }

        val pairedState = requestState(repository)
        assertTrue("state.paired is not true after the pair request", pairedState.optBoolean("paired", false))
        report("paired", pairedState)
    }

    private fun requestState(repository: NativeRepository): JSONObject = try {
        runBlocking { repository.request("state") }
    } catch (t: Throwable) {
        throw AssertionError("state request failed (details redacted)")
    }

    private fun report(phase: String, state: JSONObject) {
        val id = deviceId(state) ?: "-"
        val ip = state.optJSONObject("device")?.optString("ip").orEmpty().ifBlank { "-" }
        val line = "DevicePairingSmoke phase=$phase paired=${state.optBoolean("paired", false)} deviceId=$id ip=$ip"
        println(line)
        Log.i("DevicePairingSmoke", line)
    }

    private fun deviceId(state: JSONObject): String? = state.optJSONObject("device")
        ?.optString("id").orEmpty().ifBlank { state.optString("deviceId") }.ifBlank { null }

    private companion object {
        val HEX64 = Regex("[0-9a-fA-F]{64}")
    }
}
