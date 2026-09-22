package com.junge.connect

import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

class ChatUpdateTest {
    private val full = """{"version":"storeA:1","messages":[{"id":"a","deviceId":"mac","direction":"incoming","kind":"text","text":"hello","status":"received"}]}"""

    @Test fun unchangedRetainsHistoryAndRestartRefreshesIt() {
        val initial = decodeChatUpdate(JSONObject(full), "", emptyList())
        val cached = decodeChatUpdate(JSONObject("""{"version":"storeA:1","unchanged":true}"""), initial.version, initial.messages)
        assertSame(initial.messages, cached.messages)
        val restarted = decodeChatUpdate(JSONObject("""{"version":"storeB:1","messages":[]}"""), cached.version, cached.messages)
        assertEquals("storeB:1", restarted.version)
        assertTrue(restarted.messages.isEmpty())
    }

    @Test fun oldCoreAndInvalidVersionsDoNotEraseMessages() {
        val initial = decodeChatUpdate(JSONObject(full), "", emptyList())
        val legacy = decodeChatUpdate(JSONObject(full).apply { remove("version") }, initial.version, initial.messages)
        assertEquals("", legacy.version)
        assertSame(initial.messages, legacy.messages)
        for (invalid in listOf("""{"version":"storeB:1","unchanged":true}""", """{"unchanged":true}""", "{}")) {
            assertThrows(IllegalStateException::class.java) { decodeChatUpdate(JSONObject(invalid), initial.version, initial.messages) }
        }
    }
}
