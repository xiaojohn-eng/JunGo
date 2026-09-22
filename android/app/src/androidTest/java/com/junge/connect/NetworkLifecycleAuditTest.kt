package com.junge.connect

import android.content.Intent
import android.net.VpnService
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Assume.assumeTrue
import org.junit.Test
import org.junit.runner.RunWith

/** Opt-in, bounded lifecycle regression using the already paired test phone. */
@RunWith(AndroidJUnit4::class)
class NetworkLifecycleAuditTest {
    @Test fun latestVpnAndDeviceConnectionActionsWin() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        assumeTrue(InstrumentationRegistry.getArguments().getString("runLiveNetworkAudit") == "true")
        val context = instrumentation.targetContext
        val repository = (context.applicationContext as JunGoApplication).repository
        val activity = instrumentation.startActivitySync(Intent(context, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        try {
            assumeTrue("Existing VPN consent required", VpnService.prepare(context) == null)
            await("paired phone") { runBlocking { repository.request("state") }.optBoolean("paired") }
            fun vpn(enabled: Boolean) { context.startForegroundService(Intent(context, JunGoVpnService::class.java).putExtra("privateAccess", false).putExtra("proxy", enabled)) }
            vpn(true)
            await("VPN started") { runBlocking { repository.request("state") }.let { it.optBoolean("vpnRunning") && it.optBoolean("proxyEnabled") } }
            repeat(4) { vpn(false); vpn(true) }
            await("latest VPN start retained") { runBlocking { repository.request("state") }.let { it.optBoolean("vpnRunning") && it.optBoolean("proxyEnabled") } }
            Thread.sleep(1500)
            assertTrue(runBlocking { repository.request("state") }.optBoolean("vpnRunning"))

            context.startForegroundService(Intent(context, DeviceConnectionService::class.java).setAction(DeviceConnectionService.ACTION_PAUSE))
            await("connection paused") { repository.connectionsPaused() && !runBlocking { repository.request("state") }.optBoolean("meshRunning") }
            vpn(true)
            Thread.sleep(1500)
            assertTrue("VPN toggles must preserve an explicit device pause", repository.connectionsPaused())
            assertFalse(runBlocking { repository.request("state") }.optBoolean("meshRunning"))
            instrumentation.runOnMainSync { repository.ensureDeviceConnections(context, resume = true) }
            await("connection resumed") { !repository.connectionsPaused() && runBlocking { repository.request("state") }.optBoolean("meshRunning") }
            repeat(3) {
                instrumentation.runOnMainSync {
                    context.startForegroundService(Intent(context, DeviceConnectionService::class.java).setAction(DeviceConnectionService.ACTION_PAUSE))
                }
                await("pause preference applied") { repository.connectionsPaused() }
                instrumentation.runOnMainSync { repository.ensureDeviceConnections(context, resume = true) }
            }
            await("latest device resume retained") { !repository.connectionsPaused() && runBlocking { repository.request("state") }.optBoolean("meshRunning") }
            Thread.sleep(1500)
            assertFalse(repository.connectionsPaused())
            assertTrue(runBlocking { repository.request("state") }.optBoolean("meshRunning"))
        } finally {
            instrumentation.runOnMainSync { repository.ensureDeviceConnections(context, resume = true) }
            context.startForegroundService(Intent(context, JunGoVpnService::class.java).putExtra("privateAccess", false).putExtra("proxy", true))
            instrumentation.runOnMainSync { activity.finish() }
        }
    }

    private fun await(label: String, predicate: () -> Boolean) {
        val deadline = System.currentTimeMillis() + 60_000
        do { if (predicate()) return; Thread.sleep(200) } while (System.currentTimeMillis() < deadline)
        fail("Timed out: $label")
    }
}
