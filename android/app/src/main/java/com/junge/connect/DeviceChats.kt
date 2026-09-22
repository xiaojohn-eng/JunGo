package com.junge.connect

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.BackHandler
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.saveable.rememberSaveableStateHolder
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.window.layout.FoldingFeature
import org.json.JSONObject
import kotlinx.coroutines.delay

data class ChatMessage(val id: String, val deviceId: String, val direction: String, val kind: String,
    val text: String, val name: String, val size: Long, val completed: Long, val status: String,
    val transferId: String, val created: String, val error: String, val path: String = "") {
    companion object { fun parse(j: JSONObject) = ChatMessage(j.optString("id"), j.optString("deviceId"), j.optString("direction"), j.optString("kind"), j.optString("text"), j.optString("name"), j.optLong("size"), j.optLong("completed"), j.optString("status"), j.optString("transferId"), j.optString("created"), j.optString("error"), j.optString("path")) }
}
fun messageStatus(message: ChatMessage): String = when (message.status) {
    "queued" -> "已排队"; "waiting" -> "等待设备上线"; "hashing" -> "校验文件中"; "running" -> if (message.direction == "incoming") "接收中" else "发送中"
    "paused" -> "已暂停"; "cancelled" -> "已取消"; "failed" -> "失败"; "sent" -> "已发送"; "received" -> "已接收"
    "complete" -> if (message.direction == "incoming") "已接收" else "已发送"; else -> message.status
}

@Composable fun ChatsPage(state: Snapshot, selected: String, select: (String) -> Unit, wide: Boolean, fold: FoldingFeature?, repository: NativeRepository, openFiles: (String) -> Unit = {}) {
    val index by repository.conversations.collectAsStateWithLifecycle()
    val unread by repository.unreadCounts.collectAsStateWithLifecycle()
    val known by repository.knownPeers.collectAsStateWithLifecycle()
    val pending by repository.pendingShare.collectAsStateWithLifecycle()
    val peers = remember(index, known) {
        val knownIDs = known.map { it.id }.toSet()
        val history = index.byDevice.keys.filter { it !in knownIDs }.map { Peer(it, "历史设备 · ${it.take(8)}", "", "", "unavailable") }
        index.orderedPeers(known + history)
    }
    val peer = peers.firstOrNull { it.id == selected }
    val twoPane = wide || fold != null
    var search by rememberSaveable { mutableStateOf("") }
    val matching = remember(index, search) { index.matchingDevices(search) }
    val visible = remember(peers, search, matching) { peers.filter { search.isBlank() || it.name.contains(search, true) || it.id in matching } }
    LaunchedEffect(twoPane, peers.map { it.id }) { if (twoPane && selected.isEmpty() && peers.isNotEmpty() && pending == null) select(peers.first().id) }
    val list: @Composable () -> Unit = {
        Column(Modifier.fillMaxSize(), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            if (twoPane) Text("消息", Modifier.padding(top = 14.dp, bottom = 4.dp), style = MaterialTheme.typography.headlineSmall, fontWeight = FontWeight.Bold)
            OutlinedTextField(search, { search = it }, Modifier.fillMaxWidth(), placeholder = { Text("搜索设备或消息") }, leadingIcon = { Icon(Icons.Outlined.Search, null) }, singleLine = true, shape = MaterialTheme.shapes.medium)
            if (pending != null) EmptyCard("选择接收设备", "${pending!!.files.size} 个文件${if (pending!!.text.isNotBlank()) "及文字" else ""}已准备好。")
            if (peers.isEmpty()) EmptyCard("还没有设备会话", "配对自己的 Mac 或服务器，即可互发消息和文件。")
            LazyColumn(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                items(visible, key = { it.id }) { item ->
                    Surface(onClick = { select(item.id) }, color = if (item.id == selected) MaterialTheme.colorScheme.secondaryContainer else MaterialTheme.colorScheme.surface, shape = MaterialTheme.shapes.medium) {
                        Row(Modifier.fillMaxWidth().padding(12.dp), verticalAlignment = Alignment.CenterVertically) {
                            DeviceGlyph(item)
                            Column(Modifier.weight(1f).padding(start = 10.dp), verticalArrangement = Arrangement.spacedBy(5.dp)) {
                                val latest = index.latest[item.id]
                                Row(verticalAlignment = Alignment.CenterVertically) { Text(item.name, Modifier.weight(1f), fontWeight = FontWeight.SemiBold, maxLines = 1, overflow = TextOverflow.Ellipsis); Text(messageTime(latest?.created.orEmpty()), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
                                Row(verticalAlignment = Alignment.CenterVertically) { Text(latest?.let { if (it.kind == "text") it.text else "[文件] ${it.name}" } ?: pathLabel(item.path), Modifier.weight(1f), maxLines = 1, overflow = TextOverflow.Ellipsis, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant); val count = unread[item.id] ?: 0; if (count > 0) Badge { Text(count.toString()) } }
                            }
                        }
                    }
                }
            }
            if (twoPane) Text("仅自己的设备", Modifier.padding(bottom = 12.dp), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
    val detailState = rememberSaveableStateHolder()
    val detail: @Composable () -> Unit = {
        if (peer == null) EmptyCard("选择一台设备", "文字与文件保留在设备会话中。")
        else detailState.SaveableStateProvider(peer.id) { ChatPane(peer, index.byDevice[peer.id].orEmpty(), state.connectionsPaused, repository, if (!twoPane) ({ select("") }) else null, { openFiles(peer.id) }) }
    }
    if (twoPane) FoldAwarePanes(fold, list, detail) else if (peer == null) list() else detail()
}

@OptIn(ExperimentalLayoutApi::class)
@Composable internal fun ChatPane(peer: Peer, messages: List<ChatMessage>, paused: Boolean, repository: NativeRepository, back: (() -> Unit)?, files: () -> Unit) {
    val context = LocalContext.current
    val keyboard = LocalSoftwareKeyboardController.current
    val drafts by repository.drafts.collectAsStateWithLifecycle()
    val sendingDevices by repository.sendingMessages.collectAsStateWithLifecycle()
    val draft = drafts[peer.id].orEmpty()
    val sending = peer.id in sendingDevices
    var attachments by rememberSaveable(peer.id) { mutableStateOf(false) }
    val keyboardVisible = WindowInsets.isImeVisible
    LaunchedEffect(keyboardVisible) { if (keyboardVisible) attachments = false }
    BackHandler(enabled = attachments) { attachments = false }
    val pending by repository.pendingShare.collectAsStateWithLifecycle()
    val preparing by repository.preparations.collectAsStateWithLifecycle()
    val list = rememberLazyListState()
    var initialized by remember(peer.id) { mutableStateOf(false) }
    var followingLatest by remember(peer.id) { mutableStateOf(true) }
    LaunchedEffect(list.isScrollInProgress) { if (initialized && !list.isScrollInProgress) followingLatest = !list.canScrollForward }
    LaunchedEffect(keyboardVisible) {
        if (keyboardVisible && followingLatest && messages.isNotEmpty()) {
            delay(250) // Wait for IME resize before anchoring the last message.
            list.scrollToItem(messages.lastIndex)
        }
    }
    var more by remember { mutableStateOf(false) }
    var details by remember { mutableStateOf(false) }
    val clipboard = LocalClipboardManager.current
    var saving by rememberSaveable { mutableStateOf("") }
    val save = rememberLauncherForActivityResult(ActivityResultContracts.CreateDocument("application/octet-stream")) { uri ->
        if (uri != null) repository.prepareTransfers(context) { repository.persistUri(uri, true); repository.request("chatSaveFile", json("id" to saving, "destination" to uri.toString())); repository.refresh() }
    }
    // Capture recipient before opening the system picker; folding cannot retarget it.
    var pickerDevice by rememberSaveable { mutableStateOf(peer.id) }
    val documents = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { sendChatDocuments(context, repository, pickerDevice, it) }
    val folder = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocumentTree()) { if (it != null) sendChatFolder(context, repository, pickerDevice, it) }
    LaunchedEffect(peer.id, messages) { repository.markRead(peer.id) }
    LaunchedEffect(peer.id, messages.size, initialized) {
        if (messages.isNotEmpty()) {
            if (!initialized || (list.layoutInfo.visibleItemsInfo.lastOrNull()?.index ?: 0) >= messages.size - 3) list.scrollToItem(messages.lastIndex)
            initialized = true
        }
    }
    Column(Modifier.fillMaxSize(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Row(Modifier.fillMaxWidth().padding(vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
            if (back != null) IconButton(onClick = back) { Icon(Icons.Outlined.ArrowBack, "返回消息") }
            DeviceGlyph(peer)
            Column(Modifier.weight(1f).padding(horizontal = 8.dp)) { Text(peer.name, fontWeight = FontWeight.Bold, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis); Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(5.dp)) { StatusDot(peer.path in setOf("relay", "direct")); Text(pathLabel(peer.path), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant) } }
            IconButton(onClick = files) { Icon(Icons.Outlined.Folder, "浏览共享文件") }
            Box {
                IconButton(onClick = { more = true }) { Icon(Icons.Outlined.MoreVert, "会话操作") }
                DropdownMenu(more, { more = false }) {
                    DropdownMenuItem(text = { Text("设备信息") }, onClick = { more = false; details = true })
                    DropdownMenuItem(text = { Text("浏览共享文件") }, onClick = { more = false; files() })
                    DropdownMenuItem(text = { Text("跳到最新消息") }, onClick = { more = false; followingLatest = true; initialized = false })
                }
            }
        }
        HorizontalDivider()
        if (paused) Text("设备连接已暂停，恢复后继续收发。无需开启系统 VPN。", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error)
        if (pending != null) Surface(shape = MaterialTheme.shapes.small, color = MaterialTheme.colorScheme.secondaryContainer) { Column(Modifier.padding(10.dp)) {
            Text("待分享：${pending!!.files.size} 个文件${if (pending!!.text.isNotBlank()) "及文字" else ""}")
            Row { TextButton(onClick = { repository.sendPendingShare(context, peer.id) }) { Text("发送给此设备") }; TextButton(onClick = { repository.discardPendingShare() }) { Text("取消") } }
        } }
        if (preparing > 0) LinearProgressIndicator(Modifier.fillMaxWidth())
        LazyColumn(Modifier.weight(1f).fillMaxWidth(), state = list, contentPadding = PaddingValues(vertical = 10.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            if (messages.isEmpty()) item { EmptyCard("开始设备会话", "消息与文件自动连接；设备离线时排队，重新上线后继续。") }
            itemsIndexed(messages, key = { _, m -> m.id }) { index, message ->
                Column(Modifier.fillMaxWidth()) {
                    if (index == 0 || messageDay(messages[index - 1].created) != messageDay(message.created)) Text("${messageDay(message.created)} ${messageTime(message.created)}", Modifier.align(Alignment.CenterHorizontally).padding(bottom = 14.dp), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    MessageBubble(message, repository, { saving = message.id; save.launch(message.name) }, emphasizedOutgoing = back == null)
                }
            }
        }
        if (back == null && !keyboardVisible && !attachments) FileDropZone(peer, repository) { pickerDevice = peer.id; documents.launch(arrayOf("*/*")) }
        Row(Modifier.fillMaxWidth().padding(bottom = if (attachments) 0.dp else 8.dp), verticalAlignment = Alignment.Bottom, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            IconButton(onClick = { keyboard?.hide(); attachments = !attachments }) { Icon(if (attachments) Icons.Outlined.Close else Icons.Outlined.AddCircleOutline, "添加附件") }
            OutlinedTextField(draft, { if (it.unicodeLength() <= MESSAGE_CODEPOINT_LIMIT) { repository.saveDraft(peer.id, it) } else repository.reportError("最多 4096 字；长文字可通过系统分享分段发送。") }, Modifier.weight(1f).onFocusChanged { if (it.isFocused) attachments = false }, placeholder = { Text("发消息或文件", style = MaterialTheme.typography.bodyMedium) }, maxLines = 4, shape = MaterialTheme.shapes.medium)
            FilledIconButton(enabled = draft.isNotBlank() && !sending, onClick = {
                repository.sendText(peer.id)
            }) { Icon(Icons.Outlined.Send, "发送文字") }
        }
        if (attachments) Row(Modifier.fillMaxWidth().padding(bottom = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            AttachmentButton("照片", Icons.Outlined.Image, Modifier.weight(1f)) { pickerDevice = peer.id; attachments = false; documents.launch(arrayOf("image/*", "video/*")) }
            AttachmentButton("文件", Icons.Outlined.InsertDriveFile, Modifier.weight(1f)) { pickerDevice = peer.id; attachments = false; documents.launch(arrayOf("*/*")) }
            AttachmentButton("文件夹 ZIP", Icons.Outlined.Folder, Modifier.weight(1f)) { pickerDevice = peer.id; attachments = false; folder.launch(null) }
        }
    }
    if (details) AlertDialog(onDismissRequest = { details = false }, title = { Text(peer.name) }, text = {
        Column(verticalArrangement = Arrangement.spacedBy(10.dp)) { Text(pathLabel(peer.path)); Text("私网地址：${peer.ip.ifBlank { "尚未分配" }}"); Text("消息与文件通过设备连接传送；其他 App 使用此地址需要系统 VPN。", style = MaterialTheme.typography.bodySmall) }
    }, confirmButton = { TextButton(onClick = { details = false }) { Text("完成") } }, dismissButton = { if (peer.ip.isNotBlank()) TextButton(onClick = { clipboard.setText(AnnotatedString(peer.ip)) }) { Text("复制地址") } })
}

@Composable private fun AttachmentButton(label: String, icon: androidx.compose.ui.graphics.vector.ImageVector, modifier: Modifier, click: () -> Unit) {
    Surface(onClick = click, modifier = modifier, shape = MaterialTheme.shapes.medium, color = MaterialTheme.colorScheme.surface) { Column(Modifier.padding(vertical = 14.dp), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(6.dp)) { Icon(icon, null); Text(label, style = MaterialTheme.typography.labelMedium) } }
}

@Composable internal fun MessageBubble(message: ChatMessage, repository: NativeRepository, save: () -> Unit, emphasizedOutgoing: Boolean = false) {
    val context = LocalContext.current
    val outgoing = message.direction == "outgoing"
    val rates by repository.rates.collectAsStateWithLifecycle()
    Column(Modifier.fillMaxWidth(), horizontalAlignment = if (outgoing) Alignment.End else Alignment.Start) {
        BoxWithConstraints(Modifier.fillMaxWidth(), contentAlignment = if (outgoing) Alignment.CenterEnd else Alignment.CenterStart) {
            val bubbleWidth = minOf(380.dp, maxWidth * .86f)
            Surface(shape = MaterialTheme.shapes.medium, color = if (outgoing && emphasizedOutgoing && message.kind == "text") MaterialTheme.colorScheme.primary else if (outgoing) MaterialTheme.colorScheme.secondaryContainer else MaterialTheme.colorScheme.surface,
                modifier = if (message.kind == "text") Modifier.widthIn(max = bubbleWidth) else Modifier.width(bubbleWidth)) {
                Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(7.dp)) {
                    if (message.kind == "text") SelectionContainer { Text(message.text) }
                    else {
                        Row(verticalAlignment = Alignment.CenterVertically) { FileGlyph(message.name); Column(Modifier.weight(1f).padding(start = 10.dp)) { Text(message.name, fontWeight = FontWeight.SemiBold, maxLines = 2, overflow = TextOverflow.Ellipsis); Text(bytesLabel(message.size), style = MaterialTheme.typography.bodySmall) } }
                        if (message.status !in setOf("complete", "cancelled")) { Text("${bytesLabel(message.completed)} / ${bytesLabel(message.size)}" + if (message.size > 0) " · ${(100.0 * message.completed / message.size).toInt().coerceIn(0,100)}%" else "", style = MaterialTheme.typography.bodySmall); if (message.size > 0) LinearProgressIndicator(progress = { (message.completed.toDouble() / message.size).toFloat().coerceIn(0f, 1f) }, modifier = Modifier.fillMaxWidth()) }
                        val rate = rates[if (outgoing) message.transferId else "inbox:${message.id}"] ?: 0
                        Text(messageStatus(message) + if (rate > 0 && message.status == "running") " · ${bytesLabel(rate)}/s" else "", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                        if (!outgoing && message.status == "complete") Row(verticalAlignment = Alignment.CenterVertically) {
                            TextButton(onClick = { openChatFile(context, repository, message) }) { Text("打开") }
                            TextButton(onClick = save) { Text("另存") }
                            IconButton(onClick = { openChatFile(context, repository, message, true) }) { Icon(Icons.Outlined.Share, "分享文件", Modifier.size(18.dp)) }
                        }
                        if (outgoing && message.transferId.isNotBlank() && message.status !in setOf("complete", "cancelled")) Row { TextButton(onClick = { if (message.status in setOf("paused", "failed")) repository.prepareTransfers(context) { repository.request("transferAction", json("id" to message.transferId, "action" to "resume")); repository.refresh() } else repository.command("transferAction", json("id" to message.transferId, "action" to "pause")) }) { Text(if (message.status in setOf("paused", "failed")) "继续" else "暂停") }; TextButton(onClick = { repository.command("transferAction", json("id" to message.transferId, "action" to "cancel")) }) { Text("取消") } }
                    }
                    if (message.error.isNotBlank()) Text(message.error, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
                }
            }
        }
        Text("${messageTime(message.created)}${if (message.kind == "text") "  ${messageStatus(message)}" else ""}", Modifier.padding(top = 3.dp, start = 4.dp, end = 4.dp), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}
