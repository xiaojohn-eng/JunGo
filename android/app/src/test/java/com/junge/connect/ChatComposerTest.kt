package com.junge.connect

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

class ChatComposerTest {
    @Test fun recreationObservesPendingSendAndCannotDuplicateIt() = runBlocking {
        val persisted = mutableMapOf("mac" to "first")
        val composer = ChatComposer(persisted.toMap()) { id, text -> persisted[id] = text }
        val accepted = CompletableDeferred<Unit>()
        var calls = 0
        val job = launch(start = CoroutineStart.UNDISPATCHED) { composer.send("mac") { calls++; accepted.await() } }
        assertEquals(setOf("mac"), composer.sending.value)
        assertEquals("first", composer.drafts.value["mac"])
        assertFalse(composer.send("mac") { calls++ })
        accepted.complete(Unit); job.join()
        assertEquals(1, calls)
        assertEquals("", persisted["mac"])
        assertTrue(composer.sending.value.isEmpty())
        assertEquals("", ChatComposer(persisted) { _, _ -> }.drafts.value["mac"])
    }

    @Test fun acknowledgementPreservesNewerTextAndOtherDeviceDraft() = runBlocking {
        val composer = ChatComposer(mapOf("mac" to "first", "server" to "other")) { _, _ -> }
        val accepted = CompletableDeferred<Unit>()
        val job = launch(start = CoroutineStart.UNDISPATCHED) { composer.send("mac") { accepted.await() } }
        composer.save("mac", "next")
        accepted.complete(Unit); job.join()
        assertEquals("next", composer.drafts.value["mac"])
        assertEquals("other", composer.drafts.value["server"])
    }

    @Test fun failureAndCancellationRetainDraftAndAllowExplicitRetry() = runBlocking {
        val composer = ChatComposer(mapOf("mac" to "first")) { _, _ -> }
        try { composer.send("mac") { error("disk full") }; fail() } catch (_: IllegalStateException) {}
        assertTrue(composer.sending.value.isEmpty())
        assertEquals("first", composer.drafts.value["mac"])
        val job = launch(start = CoroutineStart.UNDISPATCHED) { composer.send("mac") { CompletableDeferred<Unit>().await() } }
        job.cancel(); job.join()
        assertTrue(composer.sending.value.isEmpty())
        assertEquals("first", composer.drafts.value["mac"])
        assertTrue(composer.send("mac") {})
        assertEquals("", composer.drafts.value["mac"])
    }
}
