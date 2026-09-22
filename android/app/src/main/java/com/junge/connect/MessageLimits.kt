package com.junge.connect

const val MESSAGE_CODEPOINT_LIMIT = 4096
const val SHARED_TEXT_CODEPOINT_LIMIT = 16384

fun String.unicodeLength(): Int = codePointCount(0, length)

/** Match Go's rune limit; never split a supplementary character's surrogate pair. */
fun takeMessageChunk(text: String): Pair<String, String> {
    val count = minOf(text.unicodeLength(), MESSAGE_CODEPOINT_LIMIT)
    val boundary = text.offsetByCodePoints(0, count)
    return text.substring(0, boundary) to text.substring(boundary)
}
