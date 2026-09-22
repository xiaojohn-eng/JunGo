package com.junge.connect

import java.time.Instant

/** Built once when message content changes, shared by all pages. */
data class ConversationIndex(
    val byDevice: Map<String, List<ChatMessage>> = emptyMap(),
    val latest: Map<String, ChatMessage> = emptyMap(),
    val latestAt: Map<String, Instant> = emptyMap()
) {
    fun orderedPeers(peers: List<Peer>): List<Peer> = peers.distinctBy { it.id }.sortedWith(
        compareByDescending<Peer> { latestAt[it.id] ?: Instant.MIN }.thenBy { it.name })

    fun unread(read: Set<String>): Map<String, Int> = byDevice.mapValues { (_, messages) ->
        messages.count { it.direction == "incoming" && it.status in setOf("received", "complete") && it.id !in read }
    }

    fun matchingDevices(query: String): Set<String> = if (query.isBlank()) byDevice.keys else byDevice.filterValues { list ->
        list.any { it.text.contains(query, true) || it.name.contains(query, true) }
    }.keys

    companion object {
        fun build(messages: List<ChatMessage>, previous: ConversationIndex? = null): ConversationIndex {
            val groups = messages.groupBy { it.deviceId }.mapValues { (id, group) ->
                previous?.byDevice?.get(id)?.takeIf { it == group } ?: group
            }
            val latest = mutableMapOf<String, ChatMessage>()
            val times = mutableMapOf<String, Instant>()
            for ((id, group) in groups) {
                if (group === previous?.byDevice?.get(id)) {
                    previous.latest[id]?.let { latest[id] = it }
                    previous.latestAt[id]?.let { times[id] = it }
                } else {
                    var time = Instant.MIN
                    for (message in group) {
                        val at = messageInstant(message.created)
                        if (at >= time) { latest[id] = message; time = at }
                    }
                    times[id] = time
                }
            }
            return ConversationIndex(groups, latest, times)
        }
    }
}

/** UI polling never controls transport delivery; native sockets stay connected. */
internal fun refreshInterval(foreground: Boolean, preparing: Int, state: Snapshot): Long = when {
    foreground || preparing > 0 || state.activeIncoming > 0 || state.transfers.any { it.active } -> 1000L
    else -> 3000L
}
