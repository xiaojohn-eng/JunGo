package com.junge.connect

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle

@Composable internal fun TransferPage(state: Snapshot, repository: NativeRepository) {
    val tasks by repository.displayedTransfers.collectAsStateWithLifecycle()
    val preparing by repository.preparations.collectAsStateWithLifecycle()
    var filter by rememberSaveable { mutableIntStateOf(0) }
    val visible = remember(tasks, filter) { tasks.filter { when (filter) { 0 -> it.status !in setOf("complete", "cancelled", "failed"); 1 -> it.status == "complete"; else -> it.status in setOf("failed", "cancelled") } } }
    LazyVerticalGrid(columns = GridCells.Adaptive(340.dp), horizontalArrangement = Arrangement.spacedBy(12.dp), verticalArrangement = Arrangement.spacedBy(12.dp), contentPadding = PaddingValues(top = 8.dp, bottom = 20.dp)) {
        item(span = { GridItemSpan(maxLineSpan) }) { SegmentedTabs(listOf("进行中", "已完成", "异常"), filter) { filter = it } }
        if (preparing > 0) item(span = { GridItemSpan(maxLineSpan) }) { LinearProgressIndicator(Modifier.fillMaxWidth()); SectionTitle("正在准备文件", "停止准备", repository::cancelTransferPreparations) }
        item(span = { GridItemSpan(maxLineSpan) }) { SectionTitle("${listOf("进行中", "已完成", "异常")[filter]} · ${visible.size}") }
        if (visible.isEmpty()) item(span = { GridItemSpan(maxLineSpan) }) { EmptyCard("这里还没有任务", "消息中的文件和共享目录传输都会显示在这里。") }
        items(visible, key = { it.id }) { TransferCard(it, repository) }
        if (filter == 0 && tasks.any { it.status == "complete" }) {
            item(span = { GridItemSpan(maxLineSpan) }) { SectionTitle("最近完成", "查看全部") { filter = 1 } }
            items(tasks.filter { it.status == "complete" }.take(2), key = { "recent:${it.id}" }) { TransferCard(it, repository) }
        }
    }
}

@Composable fun TransferCard(task: Transfer, repository: NativeRepository) {
    val peers by repository.knownPeers.collectAsStateWithLifecycle()
    val rates by repository.rates.collectAsStateWithLifecycle()
    val messages by repository.messages.collectAsStateWithLifecycle()
    val context = LocalContext.current
    var more by remember { mutableStateOf(false) }
    val remote = peers.firstOrNull { it.id == task.deviceId }?.name ?: "远端设备"
    val rate = rates[task.id] ?: 0L
    val inbox = task.id.startsWith("inbox:")
    Card(colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surface)) {
        Column(Modifier.fillMaxWidth().padding(14.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                FileGlyph(task.name)
                Column(Modifier.weight(1f).padding(horizontal = 10.dp)) {
                    Text(task.name, fontWeight = FontWeight.SemiBold, maxLines = 2, overflow = TextOverflow.Ellipsis)
                    Text(when (task.direction) { "upload" -> "手机 → $remote"; "save" -> "应用收件箱 → 所选位置"; else -> "$remote → 手机" }, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
                if (task.status == "complete") {
                    if (inbox) TextButton(onClick = { messages.firstOrNull { it.id == task.chatMessageId }?.let { openChatFile(context, repository, it) } }) { Text("打开") }
                    else if (task.destination.isNotBlank()) TextButton(onClick = { openFile(context, task.destination, task.name, repository) }) { Text("打开") }
                } else if (!inbox) {
                    IconButton(onClick = {
                        if (task.status in setOf("paused", "failed")) repository.prepareTransfers(context) {
                            repository.request("transferAction", json("id" to task.id, "action" to "resume"))
                            repository.refresh()
                        } else repository.command("transferAction", json("id" to task.id, "action" to "pause"))
                    }, enabled = task.status != "cancelled") { Icon(if (task.status in setOf("paused", "failed")) Icons.Outlined.PlayArrow else Icons.Outlined.Pause, if (task.status in setOf("paused", "failed")) "继续" else "暂停") }
                    if (task.status != "cancelled") Box { IconButton(onClick = { more = true }) { Icon(Icons.Outlined.MoreVert, "任务操作") }; DropdownMenu(more, { more = false }) { DropdownMenuItem(text = { Text("取消传输") }, onClick = { more = false; repository.command("transferAction", json("id" to task.id, "action" to "cancel")) }) } }
                }
            }
            if (task.status !in setOf("complete", "cancelled") && task.size > 0) Text("${(100.0 * task.completed / task.size).toInt().coerceIn(0,100)}%", style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.primary)
            if (task.status !in setOf("complete", "cancelled") && task.size > 0) LinearProgressIndicator(progress = { (task.completed.toDouble() / task.size).coerceIn(0.0, 1.0).toFloat() }, modifier = Modifier.fillMaxWidth())
            Text("${bytesLabel(task.completed)} / ${bytesLabel(task.size)} · ${transferStatus(task)}", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            if (rate > 0 && task.status == "running") Text("${bytesLabel(rate)}/s · 约 ${((task.size - task.completed).coerceAtLeast(0) / rate).coerceAtLeast(1)} 秒", style = MaterialTheme.typography.labelSmall)
            if (task.error.isNotEmpty()) Text(task.error, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
        }
    }
}
