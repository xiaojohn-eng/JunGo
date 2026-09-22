package com.junge.connect

import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

class ChatStateTest {
    @Test fun incomingCompletionDoesNotInventReadReceipts() {
        val incoming = ChatMessage.parse(JSONObject("""{"id":"a","deviceId":"mac","direction":"incoming","kind":"file","size":5368709120,"completed":5368709120,"status":"complete"}"""))
        assertEquals(5368709120L, incoming.size)
        assertEquals("已接收", messageStatus(incoming))
        assertEquals("等待设备上线", messageStatus(incoming.copy(status = "waiting")))
        assertEquals("已发送", messageStatus(incoming.copy(direction = "outgoing")))
    }
    @Test fun pendingShareRoundTripsWithoutLosingUnicodeOrOrder() {
        val original = PendingShare("batch", "发给我的 Mac\n文件", listOf(SharedFile("content://provider/42", "发票.pdf"), SharedFile("/private/outbox/abc", "视频.mp4")))
        assertEquals(original, PendingShare.parse(JSONObject(original.toJSON().toString())))
    }
    @Test fun activeIncomingIsSeparateFromOutgoingTransferQueue() {
        val snapshot = Snapshot.parse(JSONObject("""{"activeIncoming":2,"transfers":[]}"""))
        assertEquals(2, snapshot.activeIncoming)
        assertTrue(snapshot.transfers.isEmpty())
    }
}
