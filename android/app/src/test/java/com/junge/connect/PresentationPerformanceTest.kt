package com.junge.connect

import org.junit.Assert.*
import org.junit.Test
import java.time.Instant
import kotlin.system.measureNanoTime

class PresentationPerformanceTest {
    private fun message(id: String, device: String, created: String, status: String = "received") =
        ChatMessage(id, device, "incoming", "text", "message $id", "", 0, 0, status, "", created, "")

    @Test fun indexPreservesChronologyUnreadAndSearchAcrossDevices() {
        val messages = listOf(message("late", "a", "2026-09-22T10:00:00.123Z"),
            message("early", "a", "2026-09-22T10:00:00Z"), message("b", "b", "2026-09-22T09:00:00Z"),
            message("pending", "b", "2026-09-22T11:00:00Z", "running"))
        val index = ConversationIndex.build(messages)
        assertEquals("late", index.latest["a"]?.id)
        assertEquals(messages.take(2), index.byDevice["a"])
        assertEquals(mapOf("a" to 1, "b" to 1), index.unread(setOf("early")))
        assertEquals(setOf("a"), index.matchingDevices("late"))
        assertEquals(setOf("a", "b"), index.matchingDevices(""))
    }

    @Test fun oneDeviceProgressDoesNotRebuildAnUnchangedConversation() {
        val a = message("a", "mac", "2026-09-22T10:00:00Z")
        val b = message("b", "server", "2026-09-22T10:00:00Z", "running")
        val first = ConversationIndex.build(listOf(a, b))
        val next = ConversationIndex.build(listOf(a, b.copy(status = "complete")), first)
        assertSame(first.byDevice["mac"], next.byDevice["mac"])
        assertNotSame(first.byDevice["server"], next.byDevice["server"])
        assertEquals("complete", next.latest["server"]?.status)
        assertEquals(setOf("mac"), ConversationIndex.build(listOf(a), next).byDevice.keys)
    }

    @Test fun pollingSlowsOnlyInvisibleIdleUiAndAlwaysRecoversForTransfers() {
        assertEquals(3000L, refreshInterval(false, 0, Snapshot()))
        assertEquals(1000L, refreshInterval(true, 0, Snapshot()))
        assertEquals(1000L, refreshInterval(false, 1, Snapshot()))
        assertEquals(1000L, refreshInterval(false, 0, Snapshot(activeIncoming = 1)))
        for (status in listOf("queued", "waiting", "hashing", "running")) {
            assertEquals(1000L, refreshInterval(false, 0, Snapshot(transfers = listOf(Transfer("t", "file", "upload", 1, 0, status, "")))))
        }
    }

    @Test fun rateEstimatorDoesNotRetainCompletedHistory() {
        val history = (1..1000).map { Transfer("$it", "file", "upload", 1, 1, "complete", "") }
        assertTrue(TransferRates().update(history, 1000).isEmpty())
    }

    @Test fun benchmarkConversationProjectionWithTwentyThousandMessages() {
        val peers = (0 until 40).map { Peer("device$it", "device $it", "", "", "relay") }
        val start = Instant.parse("2026-09-22T10:00:00Z")
        val messages = (0 until 20_000).map { message("m$it", "device${it % 40}", start.plusMillis(it.toLong()).toString()) }
        val read = messages.filterIndexed { i, _ -> i % 3 == 0 }.map { it.id }.toSet()
        fun legacy(): Int {
            val latest = messages.groupBy { it.deviceId }.mapValues { (_, list) -> list.maxOf { messageInstant(it.created) } }
            val ordered = peers.sortedWith(compareByDescending<Peer> { latest[it.id] ?: Instant.MIN }.thenBy { it.name })
            return ordered.sumOf { p -> unreadMessages(messages, read, p.id) + messages.filter { it.deviceId == p.id }.size + (messages.lastOrNull { it.deviceId == p.id }?.text?.length ?: 0) }
        }
        fun value(index: ConversationIndex): Int {
            val unread = index.unread(read)
            return index.orderedPeers(peers).sumOf { (unread[it.id] ?: 0) + index.byDevice[it.id].orEmpty().size + (index.latest[it.id]?.text?.length ?: 0) }
        }
        val index = ConversationIndex.build(messages)
        assertEquals(legacy(), value(index))
        repeat(3) { sink = legacy(); sink = value(ConversationIndex.build(messages)) }
        val runs = 8
        val before = measureNanoTime { repeat(runs) { sink = legacy() } } / runs
        val cold = measureNanoTime { repeat(runs) { sink = value(ConversationIndex.build(messages)) } } / runs
        val cached = measureNanoTime { repeat(runs) { sink = value(index) } } / runs
        println("Presentation benchmark messages=20000 devices=40 legacy_ms=${before / 1e6} indexed_cold_ms=${cold / 1e6} indexed_cached_ms=${cached / 1e6}")
        // Timing is evidence, never a flaky CI pass/fail threshold.
    }
    companion object { @Volatile private var sink = 0 }
}
