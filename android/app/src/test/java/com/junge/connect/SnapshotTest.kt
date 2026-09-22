package com.junge.connect

import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

class SnapshotTest {
    @Test fun missingStateNeverClaimsConnected() {
        val state = Snapshot.parse(JSONObject("{}"))
        assertFalse(state.vpnRunning)
        assertFalse(state.meshEnabled)
        assertFalse(state.proxyEnabled)
        assertTrue(state.peers.isEmpty())
        assertTrue(state.transfers.isEmpty())
    }

    @Test fun nativeStateKeepsLargeFileOffsetsAndIndependentSwitches() {
        val state = Snapshot.parse(JSONObject("""{
            "paired":true,"meshEnabled":true,"proxyEnabled":false,"vpnRunning":true,
            "bypassUIDs":[10124],"mode":"rule",
            "peers":[{"id":"mac","name":"Mac","ip":"100.96.0.3","path":"relay"}],
            "transfers":[{"id":"file","name":"large.bin","size":5368709120,"completed":4294967297,"status":"paused"}]
        }"""))
        assertTrue(state.meshEnabled)
        assertFalse(state.proxyEnabled)
        assertEquals("relay", state.peers.single().path)
        assertEquals(5368709120L, state.transfers.single().size)
        assertEquals(4294967297L, state.transfers.single().completed)
        assertFalse(state.transfers.single().active)
        assertEquals(listOf(10124), state.bypassUIDs)
    }

    @Test fun failedOrUserStoppedTasksDoNotRequestBackgroundExecution() {
        for (status in listOf("paused", "failed", "complete", "cancelled")) {
            assertFalse(Transfer("id", "name", "upload", 5, 2, status, "").active)
        }
        for (status in listOf("queued", "hashing", "running", "waiting")) {
            assertTrue(Transfer("id", "name", "upload", 5, 2, status, "").active)
        }
    }
}
