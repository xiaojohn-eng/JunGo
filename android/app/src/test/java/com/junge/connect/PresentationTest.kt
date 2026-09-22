package com.junge.connect

import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

class PresentationTest {
    private fun message(id: String, device: String, status: String, direction: String = "incoming", created: String = "2026-09-22T10:00:00Z") =
        ChatMessage.parse(JSONObject("""{"id":"$id","deviceId":"$device","direction":"$direction","kind":"file","created":"$created","status":"$status","name":"file.zip","size":1000,"completed":650}"""))

    @Test fun recentConversationOrderIsStableAndDeduplicatesPeers() {
        val a = Peer("a", "Mac", "", "", "relay"); val b = Peer("b", "服务器", "", "", "relay")
        assertEquals(listOf(b,a), conversationOrder(listOf(a,b,a), listOf(message("1","a","received"), message("2","b","received",created="2026-09-22T11:00:00Z"))))
    }
    @Test fun nanosecondTimestampsSortChronologicallyAcrossWholeSeconds() {
        val a = Peer("a", "Mac", "", "", "relay"); val b = Peer("b", "服务器", "", "", "relay")
        assertEquals(listOf(b, a), conversationOrder(listOf(a, b), listOf(
            message("1", "a", "received", created = "2026-09-22T10:00:00Z"),
            message("2", "b", "received", created = "2026-09-22T10:00:00.123Z"))))
        assertNotEquals(messageDay("2025-09-22T10:00:00Z"), messageDay("2026-09-22T10:00:00Z"))
    }
    @Test fun unreadCountsOnlyReceivedContentAndHonorsReadState() {
        val messages = listOf(message("1","a","running"),message("2","a","complete"),message("3","a","sent","outgoing"),message("4","b","complete"))
        assertEquals(1, unreadMessages(messages, emptySet(), "a"))
        assertEquals(0, unreadMessages(messages, setOf("2"), "a"))
    }
    @Test fun inboxProjectionDoesNotDuplicateOutboundTask() {
        val native = Transfer("native", "file.zip", "upload",1000,650,"running","",chatMessageId="out")
        val result = unifiedTransfers(listOf(native), listOf(message("in","a","complete"),message("out","a","running","outgoing")))
        assertEquals(setOf("native","inbox:in"),result.map { it.id }.toSet())
        assertEquals("发送中",transferStatus(native))
        assertEquals("已接收",transferStatus(result.first { it.id=="inbox:in" }))
    }
    @Test fun delayNeverTreatsUnknownOrTimeoutAsFast() {
        assertEquals("未测速",delayLabel(-1)); assertEquals("超时",delayLabel(0)); assertEquals("86 ms",delayLabel(86))
    }
    @Test fun confirmedProgressRatesResetAfterStallOrRestart() {
        val rates=TransferRates(); val task=Transfer("a","file","upload",10000,100,"running","")
        assertEquals(0L,rates.update(listOf(task),1000)["a"])
        assertEquals(200L,rates.update(listOf(task.copy(completed=300)),2000)["a"])
        assertEquals(0L,rates.update(listOf(task.copy(completed=300)),3000)["a"])
        assertEquals(0L,rates.update(listOf(task.copy(completed=200)),4000)["a"])
        assertEquals(0L,rates.update(listOf(task.copy(completed=900)),30000)["a"])
    }
    @Test fun nativeTaskMetadataSurvivesSnapshotParsing() {
        val s=Snapshot.parse(JSONObject("""{"transfers":[{"id":"t","deviceId":"mac","destination":"content://safe/file","chatMessageId":"m","created":"2026-09-22T10:00:00Z"}],"proxies":[{"name":"group","members":["one","two"]}]}"""))
        assertEquals("mac",s.transfers.single().deviceId); assertEquals("content://safe/file",s.transfers.single().destination)
        assertEquals(listOf("one","two"),s.proxies.single().members)
    }
}
