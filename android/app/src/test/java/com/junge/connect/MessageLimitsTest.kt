package com.junge.connect

import org.junit.Assert.*
import org.junit.Test

class MessageLimitsTest {
    @Test fun nativeRuneLimitMatchesChineseAndSupplementaryCharacters() {
        val text = "军".repeat(4095) + "😀" + "后续"
        val (first, rest) = takeMessageChunk(text)
        assertEquals(4096, first.unicodeLength())
        assertTrue(first.endsWith("😀"))
        assertEquals("后续", rest)
        assertEquals(text, first + rest)
    }
    @Test fun multipleChunksPreserveEveryCharacterAndPermitResumingRemainingText() {
        val original = ("😀中a\n").repeat(3000)
        var remaining = original
        val chunks = mutableListOf<String>()
        while (remaining.isNotEmpty()) {
            val part = takeMessageChunk(remaining)
            assertTrue(part.first.unicodeLength() <= 4096)
            chunks += part.first; remaining = part.second
        }
        assertEquals(original, chunks.joinToString(""))
        assertEquals(3, chunks.size)
        assertEquals(original.removePrefix(chunks.first()), chunks.drop(1).joinToString(""))
    }
}
