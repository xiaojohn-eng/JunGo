package com.junge.connect

import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

private val timeFormat = DateTimeFormatter.ofPattern("HH:mm")
private val dayFormat = DateTimeFormatter.ofPattern("yyyy年M月d日")

fun messageTime(value: String): String = runCatching {
    timeFormat.format(Instant.parse(value).atZone(ZoneId.systemDefault()))
}.getOrDefault("")

fun messageDay(value: String): String = runCatching {
    dayFormat.format(Instant.parse(value).atZone(ZoneId.systemDefault()))
}.getOrDefault("")

internal fun messageInstant(value: String): Instant = runCatching { Instant.parse(value) }.getOrDefault(Instant.MIN)

fun conversationOrder(peers: List<Peer>, messages: List<ChatMessage>): List<Peer> =
    ConversationIndex.build(messages).orderedPeers(peers)

fun unreadMessages(messages: List<ChatMessage>, read: Set<String>, deviceId: String): Int = messages.count {
    it.deviceId == deviceId && it.direction == "incoming" && it.status in setOf("received", "complete") && it.id !in read
}

fun transferStatus(task: Transfer): String = when (task.status) {
    "queued" -> "排队中"; "waiting" -> "等待设备连接"; "hashing" -> "校验中"
    "running" -> if (task.direction == "upload") "发送中" else if (task.direction == "save") "保存中" else "接收中"
    "paused" -> "已暂停"; "failed" -> "失败"; "cancelled" -> "已取消"
    "complete" -> if (task.direction == "upload") "已发送" else if (task.direction == "save") "已保存" else "已接收"
    else -> task.status
}

// Incoming chat files are persisted by the inbox, not the outbound task queue.
// Project them once with a namespaced id; outgoing files already have native ids.
fun unifiedTransfers(tasks: List<Transfer>, messages: List<ChatMessage>): List<Transfer> =
    (tasks + messages.filter { it.kind == "file" && it.direction == "incoming" }.map {
        Transfer("inbox:${it.id}", it.name, "incoming", it.size, it.completed, it.status, it.error,
            deviceId = it.deviceId, chatMessageId = it.id, created = it.created)
    }).map { it to messageInstant(it.created) }.sortedByDescending { it.second }.map { it.first }

fun delayLabel(delay: Long) = when { delay < 0 -> "未测速"; delay == 0L -> "超时"; else -> "$delay ms" }

/** Monotonic confirmed byte deltas only. Pauses, restarts and stalls reset the estimate. */
class TransferRates {
    private data class Sample(val bytes: Long, val at: Long)
    private val samples = mutableMapOf<String, Sample>()
    fun update(tasks: List<Transfer>, now: Long): Map<String, Long> {
        val running = tasks.filter { it.status == "running" }
        samples.keys.retainAll(running.map { it.id }.toSet())
        return running.associate { task ->
            val old = samples[task.id]
            if (task.status == "running") samples[task.id] = Sample(task.completed, now) else samples.remove(task.id)
            val rate = if (task.status == "running" && old != null && now > old.at && now - old.at <= 15000 && task.completed >= old.bytes)
                (task.completed - old.bytes) * 1000 / (now - old.at) else 0
            task.id to rate
        }
    }
}
