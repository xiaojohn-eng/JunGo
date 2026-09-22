package com.junge.connect

import org.json.JSONObject

internal data class ChatUpdate(val version: String, val messages: List<ChatMessage>)

/** A restarted store has a new version namespace; old cores still return full lists. */
internal fun decodeChatUpdate(response: JSONObject, previousVersion: String, previous: List<ChatMessage>): ChatUpdate {
    val version = response.optString("version")
    if (response.optBoolean("unchanged")) {
        check(version.isNotEmpty() && version == previousVersion) { "消息版本不一致，请重试。" }
        return ChatUpdate(version, previous)
    }
    check(response.has("messages")) { "消息列表不完整，请重试。" }
    val messages = response.optJSONArray("messages").objects().map(ChatMessage::parse)
    return ChatUpdate(version, previous.takeIf { it == messages } ?: messages)
}
