package com.junge.connect

import android.content.Context
import android.net.Uri
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.saveable.rememberSaveableStateHolder
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.positionInWindow
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.dp
import androidx.documentfile.provider.DocumentFile
import androidx.window.layout.FoldingFeature
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.withContext
import kotlin.math.roundToInt

@Composable fun DevicePage(state: Snapshot, selected: String, select: (String) -> Unit, wide: Boolean, fold: FoldingFeature?, repository: NativeRepository, pair: () -> Unit) {
    val peer = state.peers.firstOrNull { it.id == selected }
    val twoPane = wide || fold != null
    LaunchedEffect(twoPane, state.peers.map { it.id }) { if (twoPane && selected.isEmpty() && state.peers.isNotEmpty()) select(state.peers.first().id) }
    val list: @Composable () -> Unit = {
        Column(Modifier.fillMaxSize()) {
            SectionTitle("我的设备", "配对", pair)
            if (state.peers.isEmpty()) EmptyCard("还没有设备", "先为手机配对，再将 Mac 或服务器加入同一组网。")
            LazyColumn(verticalArrangement = Arrangement.spacedBy(10.dp), contentPadding = PaddingValues(bottom = 20.dp)) {
                items(state.peers, key = { it.id }) { item -> PeerCard(item, selected == item.id) { select(item.id) } }
            }
        }
    }
    val detailState = rememberSaveableStateHolder()
    val detail: @Composable () -> Unit = {
        if (peer == null) EmptyCard("选择一台设备", "查看这台设备共享的文件夹和文件。")
        else detailState.SaveableStateProvider(peer.id) { FilePane(peer, repository, if (!twoPane) ({ select("") }) else null) }
    }
    if (twoPane) FoldAwarePanes(fold, list, detail)
    else if (peer == null) list() else detail()
}

// Uses the actual hinge bounds in window coordinates, never model/PPI-derived
// dimensions. Each pane is measured separately and controls cannot occupy hinge.
@Composable fun FoldAwarePanes(fold: FoldingFeature?, first: @Composable () -> Unit, second: @Composable () -> Unit) {
    var origin by remember { mutableStateOf(Offset.Zero) }
    val density = LocalDensity.current
    val gap = with(density) { 16.dp.roundToPx() }
    val preferred = with(density) { 260.dp.roundToPx() }
    Layout(content = { Box(Modifier.fillMaxSize()) { first() }; Box(Modifier.fillMaxSize()) { second() } }, modifier = Modifier.fillMaxSize().onGloballyPositioned { origin = it.positionInWindow() }) { children, constraints ->
        val width = constraints.maxWidth
        val height = constraints.maxHeight
        val horizontal = fold?.orientation == FoldingFeature.Orientation.HORIZONTAL
        val start = fold?.let { if (horizontal) (it.bounds.top - origin.y).roundToInt() else (it.bounds.left - origin.x).roundToInt() }
        val end = fold?.let { if (horizontal) (it.bounds.bottom - origin.y).roundToInt() else (it.bounds.right - origin.x).roundToInt() }
        val geometry = calculatePanes(width, height, start, end, horizontal, gap, preferred)
        val firstBounds = geometry.first
        val secondBounds = geometry.second
        val left = children[0].measure(Constraints.fixed(firstBounds.width, firstBounds.height))
        val right = children[1].measure(Constraints.fixed(secondBounds.width, secondBounds.height))
        layout(width, height) { left.place(firstBounds.x, firstBounds.y); right.place(secondBounds.x, secondBounds.y) }
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable private fun FilePane(peer: Peer, repository: NativeRepository, back: (() -> Unit)?) {
    val context = LocalContext.current
    var shareID by rememberSaveable(peer.id) { mutableStateOf("") }
    var path by rememberSaveable(peer.id) { mutableStateOf("") }
    var shares by remember(peer.id) { mutableStateOf<List<RemoteShare>>(emptyList()) }
    var listing by remember(peer.id) { mutableStateOf<RemoteListing?>(null) }
    var loading by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var refresh by remember { mutableIntStateOf(0) }
    var newDirectory by rememberSaveable { mutableStateOf(false) }
    // Launch targets survive folding/activity recreation and never follow later navigation.
    var pendingDownload by rememberSaveable { mutableStateOf<String?>(null) }
    var pendingUpload by rememberSaveable { mutableStateOf<String?>(null) }
    var pendingFolderUpload by rememberSaveable { mutableStateOf<String?>(null) }
    var pendingFolderDownload by rememberSaveable { mutableStateOf<String?>(null) }
    var search by rememberSaveable(peer.id) { mutableStateOf("") }
    var sortSize by rememberSaveable { mutableStateOf(false) }
    var preview by remember { mutableStateOf<RemoteFilePreview?>(null) }
    val location = RemoteFileTarget(peer.id, shareID, path)
    val entries = listing?.takeIf { it.location == location }?.entries.orEmpty()
    BackHandler(enabled = path.isNotEmpty()) { path = path.substringBeforeLast('/', "") }
    val readOnly = shares.firstOrNull { it.id == shareID }?.readOnly != false
    fun resultTarget(value: String?): RemoteFileTarget? = runCatching { value?.let(RemoteFileTarget::restore) }.getOrNull().also {
        if (it == null) repository.reportError("文件选择目标已失效，请返回原目录重新选择。")
    }
    val download = rememberLauncherForActivityResult(ActivityResultContracts.CreateDocument("application/octet-stream")) { uri ->
        val target = if (uri != null) resultTarget(pendingDownload) else null
        pendingDownload = null
        if (uri != null && target != null) repository.prepareTransfers(context) {
            repository.persistUri(uri, true)
            repository.request("download", target.params().put("destination", uri.toString()))
            repository.refresh()
        }
    }
    val upload = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris ->
        val target = if (uris.isNotEmpty()) resultTarget(pendingUpload) else null
        pendingUpload = null
        if (uris.isNotEmpty() && target != null) repository.prepareTransfers(context) {
            uris.forEach { uri ->
                repository.persistUri(uri)
                val name = withContext(Dispatchers.IO) { DocumentFile.fromSingleUri(context, uri)?.name } ?: error("文件名称不可用")
                requireTransferName(name)
                repository.request("upload", target.copy(path = joinPath(target.path, name)).params().put("source", uri.toString()).put("overwrite", false))
                repository.refresh()
            }
        }
    }
    val uploadFolder = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri ->
        val target = if (uri != null) resultTarget(pendingFolderUpload) else null
        pendingFolderUpload = null
        if (uri != null && target != null) repository.prepareTransfers(context) {
            repository.persistUri(uri)
            val root = DocumentFile.fromTreeUri(context, uri) ?: error("无法读取文件夹")
            val rootName = withContext(Dispatchers.IO) { root.name } ?: "文件夹"
            requireTransferName(rootName)
            queueUploadDirectory(context, repository, target.deviceId, target.shareId, joinPath(target.path, rootName), root.uri)
            repository.refresh(); refresh++
        }
    }
    val downloadFolder = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri ->
        val source = if (uri != null) resultTarget(pendingFolderDownload) else null
        pendingFolderDownload = null
        if (uri != null && source != null) repository.prepareTransfers(context) {
            repository.persistUri(uri, true)
            requireTransferName(source.name)
            val root = DocumentFile.fromTreeUri(context, uri) ?: error("无法写入文件夹")
            val target = withContext(Dispatchers.IO) { root.createDirectory(uniqueName(context, root, source.name)) } ?: error("无法创建下载目录")
            queueDownloadDirectory(context, repository, source.deviceId, source.shareId, source.path, target)
            repository.refresh()
        }
    }
    fun downloadFile(entry: RemoteEntry, target: RemoteFileTarget = location) {
        pendingDownload = target.copy(path = entry.path, name = entry.name).save()
        download.launch(entry.name)
    }
    fun downloadDirectory(folderPath: String) {
        val name = folderPath.substringAfterLast('/').ifEmpty { shares.firstOrNull { it.id == shareID }?.name ?: "共享文件" }
        pendingFolderDownload = location.copy(path = folderPath, name = name).save()
        downloadFolder.launch(null)
    }
    LaunchedEffect(peer.id, refresh, peer.path) {
        if (peer.path == "unavailable") { error = "设备离线。请确认设备已开机并恢复设备连接；无需开启系统 VPN。"; return@LaunchedEffect }
        try {
            val result = repository.request("shares", json("deviceId" to peer.id))
            shares = result.optJSONArray("shares").objects().map { RemoteShare(it.optString("id"), it.optString("name"), it.optBoolean("readOnly")) }
            if (shares.none { it.id == shareID }) { shareID = shares.firstOrNull()?.id.orEmpty(); path = "" }
            error = null
        } catch (e: Exception) { if (e is kotlinx.coroutines.CancellationException) throw e; error = e.message ?: "无法读取共享目录" }
    }
    LaunchedEffect(peer.id, shareID, path, refresh, peer.path) {
        listing = null
        preview = null
        if (shareID.isEmpty() || peer.path == "unavailable") return@LaunchedEffect
        loading = true
        try { listing = RemoteListing(location, fetchEntries(repository, peer.id, shareID, path)); error = null }
        catch (e: Exception) { if (e is kotlinx.coroutines.CancellationException) throw e; error = e.message ?: "目录读取失败" }
        finally { loading = false }
    }
    Column(Modifier.fillMaxSize(), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            if (back != null) IconButton(onClick = back) { Icon(Icons.Outlined.ArrowBack, "返回设备") }
            Column(Modifier.weight(1f)) { Text(peer.name, fontWeight = FontWeight.Bold, style = MaterialTheme.typography.titleLarge, maxLines = 2, overflow = TextOverflow.Ellipsis); Text("${pathLabel(peer.path)} · ${peer.ip}", style = MaterialTheme.typography.bodySmall) }
            IconButton(onClick = { refresh++ }) { Icon(Icons.Outlined.Refresh, "刷新文件") }
        }
        if (shares.isNotEmpty()) {
            var expanded by remember { mutableStateOf(false) }
            Box { OutlinedButton(onClick = { expanded = true }, modifier = Modifier.fillMaxWidth()) { Text(shares.firstOrNull { it.id == shareID }?.name ?: "选择共享目录", Modifier.weight(1f)); Icon(Icons.Outlined.ExpandMore, null) }
                DropdownMenu(expanded, { expanded = false }) { shares.forEach { share -> DropdownMenuItem(text = { Text("${share.name}${if (share.readOnly) " · 只读" else ""}") }, onClick = { shareID = share.id; path = ""; expanded = false }) } }
            }
            Text(if (readOnly) "只读共享 · 可以浏览与下载" else "可写共享 · 上传同名文件默认保留两份", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        if (path.isNotEmpty()) Row(verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = { path = path.substringBeforeLast('/', "") }) { Icon(Icons.Outlined.ArrowUpward, "上一级") }
            Text(path, maxLines = 2, overflow = TextOverflow.Ellipsis, style = MaterialTheme.typography.bodyMedium)
        }
        if (shareID.isNotEmpty()) Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
            Button(enabled = !readOnly && peer.path != "unavailable", onClick = { pendingUpload = location.save(); upload.launch(arrayOf("*/*")) }) { Icon(Icons.Outlined.UploadFile, null); Spacer(Modifier.width(6.dp)); Text("上传文件") }
            var more by remember { mutableStateOf(false) }
            Box {
                IconButton(onClick = { more = true }) { Icon(Icons.Outlined.MoreVert, "文件夹操作") }
                DropdownMenu(more, { more = false }) {
                    DropdownMenuItem(text = { Text("上传文件夹") }, enabled = !readOnly && peer.path != "unavailable", onClick = { more = false; pendingFolderUpload = location.save(); uploadFolder.launch(null) })
                    DropdownMenuItem(text = { Text("新建文件夹") }, enabled = !readOnly && peer.path != "unavailable", onClick = { more = false; newDirectory = true })
                    DropdownMenuItem(text = { Text("下载当前文件夹") }, enabled = peer.path != "unavailable", onClick = { more = false; downloadDirectory(path) })
                }
            }
        }
        if (error != null) ErrorCard(error!!)
        if (shares.isNotEmpty()) Row(verticalAlignment = Alignment.CenterVertically) {
            OutlinedTextField(search, { search = it }, Modifier.weight(1f), placeholder = { Text("搜索当前目录") }, leadingIcon = { Icon(Icons.Outlined.Search, null) }, singleLine = true, shape = MaterialTheme.shapes.medium)
            TextButton(onClick = { sortSize = !sortSize }) { Text(if (sortSize) "大小 ↓" else "名称 ↑") }
        }
        if (loading) LinearProgressIndicator(Modifier.fillMaxWidth())
        if (shares.isEmpty() && error == null) EmptyCard("还没有共享目录", "在这台 Mac 或服务器上选择要共享的文件夹。")
        else if (entries.isEmpty() && !loading && error == null) EmptyCard("文件夹为空", if (readOnly) "这个共享目录仅可读取。" else "点击上传文件，开始传输。")
        LazyColumn(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(8.dp), contentPadding = PaddingValues(bottom = 20.dp)) {
            items(entries.filter { it.name.contains(search, true) }.sortedWith(compareByDescending<RemoteEntry> { it.directory }.thenBy { if (sortSize) -it.size else 0 }.thenBy { it.name.lowercase() }), key = { it.path }) { entry ->
                Card(onClick = { if (entry.directory) { path = entry.path; search = "" } else preview = RemoteFilePreview(location, entry) }, enabled = peer.path != "unavailable", colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surface)) {
                    Row(Modifier.fillMaxWidth().padding(12.dp), verticalAlignment = Alignment.CenterVertically) {
                        Icon(if (entry.directory) Icons.Outlined.Folder else Icons.Outlined.InsertDriveFile, null, tint = if (entry.directory) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant)
                        Column(Modifier.weight(1f).padding(horizontal = 12.dp)) { Text(entry.name, maxLines = 2, overflow = TextOverflow.Ellipsis); Text(if (entry.directory) "文件夹" else bytesLabel(entry.size), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
                        IconButton(enabled = peer.path != "unavailable", onClick = {
                            if (entry.directory) downloadDirectory(entry.path)
                            else downloadFile(entry)
                        }) { Icon(Icons.Outlined.Download, "下载 ${entry.name}") }
                    }
                }
            }
        }
    }
    preview?.let { selected ->
        val entry = selected.entry
        AlertDialog(onDismissRequest = { preview = null }, title = { Text(entry.name) }, text = {
        Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("${peer.name} · ${bytesLabel(entry.size)}")
            Text("打开会先下载到应用缓存，进度可在传输中查看。", style = MaterialTheme.typography.bodySmall)
            TextButton(onClick = { preview = null; downloadFile(entry, selected.location) }) { Text("下载到所选位置") }
        }
    }, confirmButton = { TextButton(onClick = { preview = null; prepareFilePreview(context, repository, entry.name, "download", selected.location.copy(path = entry.path).params()) }) { Text("打开") } }, dismissButton = { TextButton(onClick = { preview = null }) { Text("取消") } }) }
    if (newDirectory) {
        var directoryName by rememberSaveable { mutableStateOf("") }
        AlertDialog(onDismissRequest = { newDirectory = false }, title = { Text("新建文件夹") }, text = { OutlinedTextField(directoryName, { directoryName = it }, label = { Text("文件夹名称") }, singleLine = true) }, confirmButton = {
            TextButton(enabled = isTransferName(directoryName), onClick = { repository.command("mkdir", json("deviceId" to peer.id, "shareId" to shareID, "path" to joinPath(path, directoryName))) { newDirectory = false; refresh++ } }) { Text("创建") }
        }, dismissButton = { TextButton(onClick = { newDirectory = false }) { Text("取消") } })
    }
}

private fun joinPath(parent: String, name: String) = if (parent.isEmpty()) name else "$parent/$name"
private suspend fun fetchEntries(repository: NativeRepository, device: String, share: String, path: String): List<RemoteEntry> =
    repository.request("entries", json("deviceId" to device, "shareId" to share, "path" to path)).optJSONArray("entries").objects()
        .map { RemoteEntry(it.optString("name"), it.optString("path"), it.optBoolean("directory"), it.optLong("size")) }
        .sortedWith(compareByDescending<RemoteEntry> { it.directory }.thenBy { it.name.lowercase() })

private suspend fun queueUploadDirectory(context: Context, repository: NativeRepository, device: String, share: String, target: String, root: Uri, depth: Int = 0) {
    currentCoroutineContext().ensureActive()
    require(depth < 64) { "文件夹层级超过 64 层" }
    repository.request("mkdir", json("deviceId" to device, "shareId" to share, "path" to target))
    val children = listTransferDocuments(context, root)
    for (child in children) {
        currentCoroutineContext().ensureActive()
        requireTransferName(child.name)
        if (child.directory) queueUploadDirectory(context, repository, device, share, joinPath(target, child.name), child.uri, depth + 1)
        else {
            repository.request("upload", json("deviceId" to device, "shareId" to share, "path" to joinPath(target, child.name), "source" to child.uri.toString(), "overwrite" to false))
            repository.refresh()
        }
    }
}

private suspend fun uniqueName(context: Context, parent: DocumentFile, requested: String): String {
    val existing = listTransferDocuments(context, parent.uri).map { it.name }.toSet()
    if (requested !in existing) return requested
    val extension = requested.substringAfterLast('.', "").let { if (it.isEmpty() || it == requested) "" else ".$it" }
    val base = requested.removeSuffix(extension)
    for (index in 1..10000) { val candidate = "$base ($index)$extension"; if (candidate !in existing) return candidate }
    error("同名文件过多，无法创建新文件")
}

private suspend fun queueDownloadDirectory(context: Context, repository: NativeRepository, device: String, share: String, source: String, target: DocumentFile, depth: Int = 0) {
    currentCoroutineContext().ensureActive()
    require(depth < 64) { "文件夹层级超过 64 层" }
    for (entry in fetchEntries(repository, device, share, source)) {
        currentCoroutineContext().ensureActive()
        requireTransferName(entry.name)
        val child = withContext(Dispatchers.IO) { if (entry.directory) target.createDirectory(uniqueName(context, target, entry.name)) else target.createFile("application/octet-stream", uniqueName(context, target, entry.name)) } ?: error("无法创建 ${entry.name}")
        if (entry.directory) queueDownloadDirectory(context, repository, device, share, entry.path, child, depth + 1)
        else { repository.request("download", json("deviceId" to device, "shareId" to share, "path" to entry.path, "destination" to child.uri.toString())); repository.refresh() }
    }
}
