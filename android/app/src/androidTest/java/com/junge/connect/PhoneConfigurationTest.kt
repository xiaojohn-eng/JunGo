package com.junge.connect

import android.util.Log
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import java.security.MessageDigest
import org.json.JSONObject
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assume
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Bounded real-device phone configuration smoke test.
 *
 * Input is the one-shot, locally pushed app-private `qa-phone-config.json`. It is parsed
 * and removed in `finally` before any native work, so the cleanup boundary holds no matter
 * how the device behaves. Only the branches the input actually supplies run:
 *
 *  - `profile` (required for the profile step) is handed to the production
 *    `NativeRepository.request("importProfile")` unchanged; its subscription URL and any
 *    node credential stay in memory and are never printed, logged or attached to a failure.
 *  - `phoneMessage` {deviceId,text} uses production `chatSendText` and then polls the real
 *    `chatList` payload until that message reaches its terminal success status.
 *  - `phoneFile` {deviceId} generates a 1 MiB deterministic file inside the app cache,
 *    sends it with production `chatSendFile`, and waits the same way.
 *
 * The one network call raised here is `network(mesh=true, proxy=false)`. The system VPN is
 * never started, no URL, node credential or raw state is ever logged, the imported
 * production configuration is deliberately kept, and a skipped or stopped branch never
 * reports success. Failure messages carry a phase name only - never a payload, a state
 * dump or an exception chain.
 */
@RunWith(AndroidJUnit4::class)
class PhoneConfigurationTest {
    @Test
    fun importProfileThroughNativeRepositoryAndVerifyPhoneDelivery() {
        val targetContext = InstrumentationRegistry.getInstrumentation().targetContext
        val repository = (targetContext.applicationContext as JunGoApplication).repository

        val input = File(targetContext.filesDir, QA_INPUT)
        Assume.assumeTrue("$QA_INPUT not present; skipping phone configuration smoke test", input.isFile)

        val params = try {
            JSONObject(input.readText(Charsets.UTF_8))
        } catch (t: Throwable) {
            // JSONObject exceptions can embed the raw payload; never surface them.
            throw AssertionError("$QA_INPUT could not be read as a JSON object (details redacted)")
        } finally {
            // One-shot input: always removed, success or failure, before any state work.
            input.delete()
        }

        // 1. The phone must already be paired, and the only network call this test raises is
        //    the private device connection: mesh on, proxy off. Skipping here is reported as
        //    a skip, never as a pass.
        val start = requestState(repository, "start")
        Assume.assumeTrue("Phone is not paired; skipping phone configuration smoke test", start.optBoolean("paired", false))
        native(repository, "network start") { repository.request("network", json("mesh" to true, "proxy" to false)) }
        val connected = requestState(repository, "network start")
        assertTrue("state.paired is not true; the phone cannot carry a private message", connected.optBoolean("paired", false))
        assertTrue("state.meshRunning is not true after network(mesh=true, proxy=false)", connected.optBoolean("meshRunning", false))
        assertFalse("the system VPN must stay off for this test", connected.optBoolean("vpnRunning", false))

        // 2. Import the production configuration through the same repository entry point the
        //    UI uses. The request parameters come from the supplied JSON object as-is, so no
        //    subscription URL or node credential is ever written into this source file.
        val profile = params.optJSONObject("profile")
            ?: throw AssertionError("no profile object in $QA_INPUT (details redacted)")
        native(repository, "importProfile") { repository.request("importProfile", profile) }
        val imported = requestState(repository, "importProfile")
        assertTrue("state.profileURL is empty after importProfile; the configuration was not kept", imported.optString("profileURL").isNotEmpty())
        // Only the boolean leaves the device: never the URL, node list or full state.
        val label = safeLabel(params.optString("label", "").ifBlank { profile.optString("label", "") })
        report("profileLoaded=true" + if (label.isEmpty()) "" else " label=$label")

        // 3. Optional text delivery, verified against the real chatList payload.
        val message = params.optJSONObject("phoneMessage")
        if (message == null) {
            println("$TAG phoneMessage=skipped (no input)")
        } else {
            val deviceId = message.optString("deviceId").trim()
            val text = message.optString("text")
            assertTrue("phoneMessage.deviceId is required", deviceId.isNotEmpty())
            assertTrue("phoneMessage.text is required", text.isNotBlank())
            val sent = nativeJson(repository, "chatSendText") { repository.request("chatSendText", json("deviceId" to deviceId, "text" to text)) }
            val id = sent.optString("id")
            assertTrue("chatSendText did not return a message id", id.isNotEmpty())
            awaitOutgoingMessage(repository, id, "chatSendText")
            report("phoneMessageSent=true messageId=$id")
        }

        // 4. Optional file delivery. The payload is generated here so its digest is
        //    reproducible, and it is removed again whatever the transfer does.
        val attachment = params.optJSONObject("phoneFile")
        if (attachment == null) {
            println("$TAG phoneFile=skipped (no input)")
        } else {
            val deviceId = attachment.optString("deviceId").trim()
            assertTrue("phoneFile.deviceId is required", deviceId.isNotEmpty())
            val payload = File(targetContext.cacheDir, TEST_FILE_NAME)
            try {
                writeDeterministicFile(payload)
                val digest = sha256(payload)
                val sent = nativeJson(repository, "chatSendFile") {
                    repository.request("chatSendFile", json("deviceId" to deviceId, "source" to payload.absolutePath, "name" to payload.name))
                }
                val id = sent.optString("id")
                assertTrue("chatSendFile did not return a message id", id.isNotEmpty())
                awaitOutgoingMessage(repository, id, "chatSendFile")
                report("phoneFileSent=true testFileSha256=$digest messageId=$id")
            } finally {
                // The generated test payload is never part of the production state.
                payload.delete()
            }
        }
    }

    private fun requestState(repository: NativeRepository, phase: String): JSONObject = try {
        kotlinx.coroutines.runBlocking { repository.request("state") }
    } catch (t: Throwable) {
        throw AssertionError("state request failed during $phase (details redacted)")
    }

    private fun native(repository: NativeRepository, phase: String, block: suspend () -> Unit) {
        try {
            kotlinx.coroutines.runBlocking { block() }
        } catch (t: Throwable) {
            val detail = t.message.orEmpty().lowercase()
            val categories = listOf("yaml", "unmarshal", "https", "certificate", "tls", "timeout", "resolve", "provider", "geodata", "geoip", "geosite", "规则", "订阅下载失败", "配置为空", "proxy", "rule", "404", "403").filter { detail.contains(it) }
            throw AssertionError("native call failed during $phase (categories=${categories.joinToString(",")}; details redacted)")
        }
    }

    private fun nativeJson(repository: NativeRepository, phase: String, block: suspend () -> JSONObject): JSONObject = try {
        kotlinx.coroutines.runBlocking { block() }
    } catch (t: Throwable) {
        throw AssertionError("native call failed during $phase (details redacted)")
    }

    /**
     * Polls the production `chatList` reply until the outgoing message with [messageId]
     * reaches a terminal success status, bounded by [POLL_TIMEOUT_MILLIS]. The real engine
     * stores `sent` for a delivered text message and mirrors the transfer status for a file
     * message, whose success terminal state is `complete`; both are accepted, and neither a
     * failure nor a stall is ever treated as success.
     */
    private fun awaitOutgoingMessage(repository: NativeRepository, messageId: String, phase: String) {
        val deadline = System.currentTimeMillis() + POLL_TIMEOUT_MILLIS
        var lastStatus = "unknown"
        while (System.currentTimeMillis() < deadline) {
            val message = findOutgoingMessage(repository, messageId, phase)
            val status = message?.optString("status").orEmpty()
            if (status.isNotEmpty()) lastStatus = status
            if (status in SUCCESS_STATUSES) return
            if (status in FAILURE_STATUSES) throw AssertionError("$phase reached the terminal status $status (details redacted)")
            Thread.sleep(POLL_INTERVAL_MILLIS)
        }
        throw AssertionError("$phase did not reach a terminal success status within ${POLL_TIMEOUT_MILLIS / 1000}s; last status was $lastStatus")
    }

    /** Only the message status of the outgoing message this test created is inspected. */
    private fun findOutgoingMessage(repository: NativeRepository, messageId: String, phase: String): JSONObject? {
        val list = nativeJson(repository, phase) { repository.request("chatList") }
        val messages = list.optJSONArray("messages") ?: return null
        for (index in 0 until messages.length()) {
            val message = messages.optJSONObject(index) ?: continue
            if (message.optString("id") == messageId && message.optString("direction") == "outgoing") return message
        }
        return null
    }

    /** 1 MiB of deterministic bytes: byte[i] = (i * 31 + 7) mod 256. */
    private fun writeDeterministicFile(file: File) {
        val buffer = ByteArray(64 * 1024)
        var written = 0
        file.outputStream().use { out ->
            while (written < TEST_FILE_BYTES) {
                for (offset in buffer.indices) buffer[offset] = (((written + offset) * 31 + 7) and 0xFF).toByte()
                val chunk = minOf(buffer.size, TEST_FILE_BYTES - written)
                out.write(buffer, 0, chunk)
                written += chunk
            }
        }
    }

    private fun sha256(file: File): String {
        val digest = MessageDigest.getInstance("SHA-256")
        file.inputStream().use { input ->
            val buffer = ByteArray(64 * 1024)
            while (true) {
                val read = input.read(buffer)
                if (read <= 0) break
                digest.update(buffer, 0, read)
            }
        }
        return digest.digest().joinToString("") { "%02x".format(it.toInt() and 0xFF) }
    }

    /**
     * Echoes at most [SAFE_LABEL_MAX] characters of the supplied configuration name and only
     * when every character is plainly safe, so an input can never smuggle a subscription URL,
     * a node credential or a newline into the log line.
     */
    private fun safeLabel(value: String): String {
        val candidate = value.trim()
        if (candidate.isEmpty() || candidate.length > SAFE_LABEL_MAX) return ""
        return if (SAFE_LABEL.matches(candidate)) candidate else ""
    }

    private fun report(line: String) {
        println("$TAG $line")
        Log.i(TAG, line)
    }

    private companion object {
        const val TAG = "PhoneConfigurationSmoke"
        const val QA_INPUT = "qa-phone-config.json"
        const val TEST_FILE_NAME = "jungo-qa-1mib.bin"
        const val TEST_FILE_BYTES = 1 shl 20
        const val POLL_TIMEOUT_MILLIS = 60_000L
        const val POLL_INTERVAL_MILLIS = 1_000L
        const val SAFE_LABEL_MAX = 32
        val SAFE_LABEL = Regex("[A-Za-z0-9._\\-\\u4e00-\\u9fff]+")
        val SUCCESS_STATUSES = setOf("sent", "complete")
        val FAILURE_STATUSES = setOf("failed", "cancelled")
    }
}
