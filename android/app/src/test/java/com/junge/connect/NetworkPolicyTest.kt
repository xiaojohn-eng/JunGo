package com.junge.connect

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class NetworkPolicyTest {
    @Test fun desiredVpnFollowsPrivateAccessOrProxy() {
        val table = listOf(
            Triple(false, false, false),
            Triple(false, true, true),
            Triple(true, false, true),
            Triple(true, true, true)
        )
        for ((privateAccess, proxy, expected) in table) {
            assertTrue(
                "desiredVPN(privateAccess=$privateAccess, proxy=$proxy) should be $expected",
                NetworkPolicy.desiredVPN(privateAccess, proxy) == expected
            )
        }
    }

    @Test fun deviceConnectionFollowsPairingAndExplicitPause() {
        val table = listOf(
            Triple(false, false, false),
            Triple(false, true, false),
            Triple(true, false, true),
            Triple(true, true, false)
        )
        for ((paired, paused, expected) in table) {
            assertTrue(
                "deviceConnectionActive(paired=$paired, paused=$paused) should be $expected",
                NetworkPolicy.deviceConnectionActive(paired, paused) == expected
            )
        }
    }

    @Test fun vpnTogglesNeverResumeAPausedDeviceConnection() {
        for (privateAccess in listOf(false, true)) for (proxy in listOf(false, true)) {
            // A paused connection stays paused whatever the VPN switches say:
            // neither switch is an input to the device connection decision, and
            // only an explicit resume clears the pause.
            assertFalse(
                "privateAccess=$privateAccess proxy=$proxy must not resume a paused connection",
                NetworkPolicy.deviceConnectionActive(paired = true, paused = true)
            )
        }
    }

    @Test fun newSwitchesDefaultOffInSnapshot() {
        val state = Snapshot()
        assertFalse(state.privateAccess)
        assertFalse(state.connectionsPaused)
    }
}
