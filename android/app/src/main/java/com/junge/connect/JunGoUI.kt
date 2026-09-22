package com.junge.connect

import android.content.Intent
import androidx.activity.compose.BackHandler
import androidx.activity.compose.LocalActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.saveable.rememberSaveableStateHolder
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.window.layout.DisplayFeature
import androidx.window.layout.FoldingFeature
import androidx.window.layout.WindowInfoTracker
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.map

@Composable fun JunGoTheme(content: @Composable () -> Unit) {
    val colors = if (isSystemInDarkTheme()) darkColorScheme(primary = Color(0xFF9ABDFF)) else lightColorScheme(
        primary = Color(0xFF246BFD), background = Color(0xFFF6F7FA), surface = Color.White,
        onSurface = Color(0xFF171D2B), surfaceContainer = Color(0xFFF0F3F8), secondaryContainer = Color(0xFFE8F0FF),
        surfaceContainerLow = Color.White, surfaceContainerHigh = Color.White, surfaceContainerHighest = Color.White)
    MaterialTheme(colorScheme = colors, shapes = Shapes(small = RoundedCornerShape(10.dp), medium = RoundedCornerShape(16.dp), large = RoundedCornerShape(20.dp)), content = content)
}

private enum class Page(val label: String, val icon: ImageVector) {
    DEVICES("设备", Icons.Outlined.Devices), CHATS("消息", Icons.Outlined.ChatBubbleOutline), NETWORK("网络", Icons.Outlined.Language), TRANSFERS("传输", Icons.Outlined.SwapVert)
}

@OptIn(ExperimentalMaterial3Api::class, ExperimentalLayoutApi::class)
@Composable fun JunGoApp(repository: NativeRepository, network: (Boolean, Boolean) -> Unit) {
    val state by repository.state.collectAsStateWithLifecycle()
    val error by repository.error.collectAsStateWithLifecycle()
    val unread by repository.unreadCounts.collectAsStateWithLifecycle()
    val known by repository.knownPeers.collectAsStateWithLifecycle()
    val activity = LocalActivity.current
    val features by remember(activity) {
        if (activity == null) flowOf(emptyList<DisplayFeature>()) else WindowInfoTracker.getOrCreate(activity).windowLayoutInfo(activity).map { it.displayFeatures }
    }.collectAsState(initial = emptyList())
    val fold = features.filterIsInstance<FoldingFeature>().firstOrNull { it.isSeparating || it.occlusionType == FoldingFeature.OcclusionType.FULL }
    var pageName by rememberSaveable { mutableStateOf(Page.DEVICES.name) }
    var selectedDevice by rememberSaveable { mutableStateOf("") }
    var selectedChat by rememberSaveable { mutableStateOf("") }
    var pair by rememberSaveable { mutableStateOf(false) }
    var settings by rememberSaveable { mutableStateOf(false) }
    val pending by repository.pendingShare.collectAsStateWithLifecycle()
    LaunchedEffect(pending?.id) { if (pending != null) { pageName = Page.CHATS.name; selectedChat = "" } }
    val page = Page.valueOf(pageName)
    val snack = remember { SnackbarHostState() }
    val holder = rememberSaveableStateHolder()
    LaunchedEffect(error) { error?.let { snack.showSnackbar(it, "知道了"); repository.clearError() } }
    BoxWithConstraints(Modifier.fillMaxSize()) {
        val compact = maxWidth < 600.dp
        val wide = maxWidth >= 840.dp || (fold?.orientation == FoldingFeature.Orientation.VERTICAL && maxWidth >= 600.dp)
        val focused = (page == Page.CHATS && selectedChat.isNotEmpty()) || (page == Page.DEVICES && selectedDevice.isNotEmpty())
        val ime = WindowInsets.isImeVisible
        BackHandler(enabled = (page != Page.DEVICES || selectedDevice.isNotEmpty()) && !pair && !settings) {
            if (page == Page.CHATS && selectedChat.isNotEmpty() && !wide) selectedChat = ""
            else if (page == Page.DEVICES && selectedDevice.isNotEmpty()) selectedDevice = ""
            else pageName = Page.DEVICES.name
        }
        Scaffold(contentWindowInsets = WindowInsets.safeDrawing,
            snackbarHost = { SnackbarHost(snack) },
            topBar = { if (!focused && !(wide && page == Page.CHATS)) TopAppBar(title = { Text(page.label, fontWeight = FontWeight.Bold) }, actions = {
                if (page == Page.DEVICES) IconButton(onClick = { pair = true }) { Icon(Icons.Outlined.AddLink, "配对设备") }
                IconButton(onClick = { settings = true }) { Icon(Icons.Outlined.Settings, "设置") }
            }, colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background)) },
            bottomBar = { if (compact && !focused && !ime) NavigationBar(containerColor = MaterialTheme.colorScheme.surface) {
                Page.entries.forEach { item -> NavigationBarItem(selected = page == item, onClick = { pageName = item.name }, icon = {
                    BadgedBox(badge = { if (item == Page.CHATS) { val count = unread.values.sum(); if (count > 0) Badge { Text(count.toString()) } } }) { Icon(item.icon, item.label) }
                }, label = { Text(item.label) }) }
            } }
        ) { insets ->
            // Consume Scaffold insets before applying the remaining IME inset.
            Row(Modifier.fillMaxSize().padding(insets).consumeWindowInsets(insets).imePadding()) {
                if (!compact) NavigationRail(containerColor = MaterialTheme.colorScheme.surface) {
                    Page.entries.forEach { item -> NavigationRailItem(selected = page == item, onClick = { pageName = item.name }, icon = { Icon(item.icon, item.label) }, label = { Text(item.label) }, modifier = Modifier.padding(vertical = 10.dp)) }
                    Spacer(Modifier.weight(1f)); IconButton(onClick = { settings = true }) { Icon(Icons.Outlined.Settings, "设置") }
                }
                Box(Modifier.weight(1f).fillMaxHeight().padding(horizontal = if (compact) 16.dp else 20.dp)) {
                    holder.SaveableStateProvider(pageName) {
                        when (page) {
                            Page.DEVICES -> if (selectedDevice.isEmpty()) DeviceOverview(state, repository, { pair = true }, { selectedDevice = it }, { selectedChat = it; pageName = Page.CHATS.name }, { pageName = Page.TRANSFERS.name }, { pageName = Page.NETWORK.name })
                                else DevicePage(state.copy(peers = known), selectedDevice, { selectedDevice = it }, wide, fold, repository, { pair = true })
                            Page.CHATS -> ChatsPage(state, selectedChat, { selectedChat = it }, wide, fold, repository, { selectedDevice = it; pageName = Page.DEVICES.name })
                            Page.NETWORK -> NetworkPage(state, repository, network)
                            Page.TRANSFERS -> TransferPage(state, repository)
                        }
                    }
                }
            }
        }
    }
    if (pair) PairDialog(repository) { pair = false }
    if (settings) SettingsDialog(state, repository, { pair = true; settings = false }) { settings = false }
}

@Composable internal fun DeviceOverview(state: Snapshot, repository: NativeRepository, pair: () -> Unit, files: (String) -> Unit, chat: (String) -> Unit, transfers: () -> Unit, network: () -> Unit) {
    val peers by repository.knownPeers.collectAsStateWithLifecycle()
    val transfersList by repository.displayedTransfers.collectAsStateWithLifecycle()
    val context = LocalContext.current
    LazyVerticalGrid(columns = GridCells.Adaptive(340.dp), horizontalArrangement = Arrangement.spacedBy(12.dp), verticalArrangement = Arrangement.spacedBy(12.dp), contentPadding = PaddingValues(top = 8.dp, bottom = 20.dp)) {
        item(span = { GridItemSpan(maxLineSpan) }) { Surface(color = MaterialTheme.colorScheme.surface, shape = MaterialTheme.shapes.medium) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                StatusDot(state.meshRunning && !state.connectionsPaused)
                Text(if (!state.paired) "尚未配对" else if (state.connectionsPaused) "设备连接已暂停" else if (state.meshRunning) "设备连接正常" else "设备连接中", Modifier.weight(1f).padding(start = 8.dp), style = MaterialTheme.typography.labelLarge)
                TextButton(onClick = network) { Text(if (state.proxyEnabled && state.vpnRunning) "代理已开启" else "代理已关闭") }
            }
        } }
        if (!state.paired) item(span = { GridItemSpan(maxLineSpan) }) { Column { EmptyCard("连接自己的设备", "配对 Mac 或服务器，即可发送消息与文件。无需开启系统 VPN。"); TextButton(onClick = pair) { Text("配对设备") } } }
        if (state.connectionsPaused) item(span = { GridItemSpan(maxLineSpan) }) { TextButton(onClick = { repository.ensureDeviceConnections(context, true) }) { Text("恢复设备连接") } }
        if (state.engineError.isNotEmpty()) item(span = { GridItemSpan(maxLineSpan) }) { ErrorCard(state.engineError) }
        items(peers, key = { it.id }) { peer -> Card(colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surface)) {
            Row(Modifier.fillMaxWidth().clickable { files(peer.id) }.padding(14.dp), verticalAlignment = Alignment.CenterVertically) {
                DeviceGlyph(peer); Column(Modifier.weight(1f).padding(horizontal = 12.dp)) { Text(peer.name, fontWeight = FontWeight.SemiBold); Text(pathLabel(peer.path), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }; Icon(Icons.Outlined.ChevronRight, null)
            }
            Row(Modifier.padding(start = 12.dp, end = 12.dp, bottom = 10.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                FilledTonalButton(onClick = { chat(peer.id) }, modifier = Modifier.weight(1f), shape = MaterialTheme.shapes.small) { Icon(Icons.Outlined.ChatBubbleOutline, null, Modifier.size(18.dp)); Spacer(Modifier.width(6.dp)); Text("发消息") }
                FilledTonalButton(onClick = { files(peer.id) }, modifier = Modifier.weight(1f), shape = MaterialTheme.shapes.small) { Icon(Icons.Outlined.Folder, null, Modifier.size(18.dp)); Spacer(Modifier.width(6.dp)); Text("浏览文件") }
            }
        } }
        item(span = { GridItemSpan(maxLineSpan) }) { SectionTitle("最近传输", "查看全部", transfers) }
        val tasks = transfersList.take(3)
        if (tasks.isEmpty()) item(span = { GridItemSpan(maxLineSpan) }) { EmptyCard("文件随设备流动", "在消息里添加文件，或浏览设备的共享目录。") }
        items(tasks, key = { it.id }) { TransferCard(it, repository) }
    }
}

@Composable fun StatusDot(online: Boolean) { Box(Modifier.size(8.dp).background(if (online) Color(0xFF1CA75B) else MaterialTheme.colorScheme.outline, RoundedCornerShape(50))) }
@Composable fun FileGlyph(name: String) {
    val extension = name.substringAfterLast('.', "").lowercase()
    val tint = when (extension) { "pdf" -> Color(0xFFE9434F); "zip", "7z", "rar" -> MaterialTheme.colorScheme.primary; else -> MaterialTheme.colorScheme.primary }
    Column(horizontalAlignment = Alignment.CenterHorizontally) {
        Icon(if (extension in setOf("zip", "7z", "rar")) Icons.Outlined.FolderZip else Icons.Outlined.InsertDriveFile, null, Modifier.size(30.dp), tint = tint)
        if (extension in setOf("pdf", "zip", "7z", "rar")) Text(extension.uppercase(), style = MaterialTheme.typography.labelSmall, color = tint)
    }
}

@Composable fun SegmentedTabs(labels: List<String>, selected: Int, select: (Int) -> Unit) {
    Surface(shape = RoundedCornerShape(50), color = MaterialTheme.colorScheme.surfaceContainer) {
        Row(Modifier.fillMaxWidth().padding(4.dp)) {
            labels.forEachIndexed { index, label ->
                Surface(onClick = { select(index) }, modifier = Modifier.weight(1f).semantics { this.selected = selected == index; role = Role.Tab }, shape = RoundedCornerShape(50),
                    color = if (selected == index) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.surfaceContainer) {
                    Box(Modifier.padding(horizontal = 8.dp, vertical = 12.dp), contentAlignment = Alignment.Center) {
                        Text(label, style = MaterialTheme.typography.labelLarge, fontWeight = if (selected == index) FontWeight.SemiBold else FontWeight.Normal)
                    }
                }
            }
        }
    }
}
fun deviceIcon(peer: Peer): ImageVector = when {
    listOf("服务器", "server", "linux").any { peer.name.contains(it, true) } -> Icons.Outlined.Dns
    listOf("手机", "phone", "android", "SM-").any { peer.name.contains(it, true) } -> Icons.Outlined.Smartphone
    else -> Icons.Outlined.LaptopMac
}
@Composable fun DeviceGlyph(peer: Peer) { Surface(shape = MaterialTheme.shapes.small, color = MaterialTheme.colorScheme.secondaryContainer) { Icon(deviceIcon(peer), null, Modifier.padding(9.dp).size(24.dp), tint = MaterialTheme.colorScheme.primary) } }
@Composable fun SectionTitle(title: String, action: String = "", click: () -> Unit = {}) { Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) { Text(title, Modifier.weight(1f), style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold); if (action.isNotEmpty()) TextButton(onClick = click) { Text(action) } } }
@Composable fun EmptyCard(title: String, body: String) { Surface(shape = MaterialTheme.shapes.medium, color = MaterialTheme.colorScheme.surface) { Column(Modifier.fillMaxWidth().padding(18.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) { Text(title, fontWeight = FontWeight.SemiBold); Text(body, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant) } } }
@Composable fun ErrorCard(text: String) { Surface(color = MaterialTheme.colorScheme.errorContainer, shape = MaterialTheme.shapes.medium) { Text(text, Modifier.fillMaxWidth().padding(12.dp), color = MaterialTheme.colorScheme.onErrorContainer, style = MaterialTheme.typography.bodySmall) } }
@Composable fun PeerCard(peer: Peer, selected: Boolean, click: () -> Unit) { Card(onClick = click, colors = CardDefaults.cardColors(containerColor = if (selected) MaterialTheme.colorScheme.secondaryContainer else MaterialTheme.colorScheme.surface)) { Row(Modifier.fillMaxWidth().padding(12.dp), verticalAlignment = Alignment.CenterVertically) { DeviceGlyph(peer); Column(Modifier.weight(1f).padding(horizontal = 10.dp)) { Text(peer.name, fontWeight = FontWeight.SemiBold); Text(pathLabel(peer.path), style = MaterialTheme.typography.bodySmall) }; Icon(Icons.Outlined.ChevronRight, null) } } }
fun pathLabel(path: String) = when (path) { "direct" -> "在线 · 直连"; "relay" -> "在线 · 中继"; else -> "离线 · 等待连接" }
fun deviceConnectionLabel(state: Snapshot) = when { state.connectionsPaused -> "已暂停"; state.meshRunning -> "在线"; else -> "连接中" }
fun modeLabel(mode: String) = when (mode) { "global" -> "全局模式"; "direct" -> "直连模式"; else -> "规则模式" }
fun bytesLabel(value: Long): String { if (value < 0) return "大小未知"; if (value < 1024) return "$value B"; val units = listOf("KB", "MB", "GB", "TB"); var n = value.toDouble(); var i = -1; do { n /= 1024; i++ } while (n >= 1024 && i < units.lastIndex); return "%.1f %s".format(n, units[i]) }

@Composable private fun SettingsDialog(state: Snapshot, repository: NativeRepository, pair: () -> Unit, close: () -> Unit) {
    val context = LocalContext.current
    AlertDialog(onDismissRequest = close, title = { Text("设置") }, text = { Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Text(state.deviceName.ifBlank { "本机尚未配对" }, fontWeight = FontWeight.Bold)
        Text("设备消息与文件", fontWeight = FontWeight.SemiBold)
        Text("${deviceConnectionLabel(state)} · 无需开启系统 VPN。优先直连，无法直连时使用加密中继。")
        TextButton(enabled = state.paired, onClick = {
            if (state.connectionsPaused) repository.ensureDeviceConnections(context, true)
            else runCatching { context.startService(Intent(context, DeviceConnectionService::class.java).setAction(DeviceConnectionService.ACTION_PAUSE)) }.onFailure { repository.reportError("暂停失败：${it.message}") }
        }) { Text(if (state.connectionsPaused) "恢复设备连接" else "暂停设备连接") }
        HorizontalDivider(); TextButton(onClick = pair) { Text("配对设备") }
        Text("文件通过系统选择器授权，只能访问共享目录。设备离线时等待恢复，主动暂停后不会自动继续。", style = MaterialTheme.typography.bodySmall)
        Text("军哥互联 ${BuildConfig.VERSION_NAME}", style = MaterialTheme.typography.labelMedium)
    } }, confirmButton = { TextButton(onClick = close) { Text("完成") } })
}
