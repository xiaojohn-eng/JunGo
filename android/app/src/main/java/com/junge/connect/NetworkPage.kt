package com.junge.connect

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.horizontalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch

@OptIn(ExperimentalLayoutApi::class)
@Composable internal fun NetworkPage(state: Snapshot, repository: NativeRepository, network: (Boolean, Boolean) -> Unit) {
    var importing by rememberSaveable { mutableStateOf(false) }
    var bypass by rememberSaveable { mutableStateOf(false) }
    var privateOptions by rememberSaveable { mutableStateOf(false) }
    var query by rememberSaveable { mutableStateOf("") }
    var favoritesOnly by rememberSaveable { mutableStateOf(false) }
    var group by rememberSaveable { mutableStateOf("") }
    var favorites by remember { mutableStateOf(repository.uiPreferences.getStringSet("favorites", emptySet()).orEmpty().toSet()) }
    var label by rememberSaveable { mutableStateOf(repository.uiPreferences.getString("profileName", "当前订阅").orEmpty()) }
    var rename by rememberSaveable { mutableStateOf(false) }
    var renameDraft by rememberSaveable { mutableStateOf("") }
    var profileMenu by remember { mutableStateOf(false) }
    var groupMenu by remember { mutableStateOf(false) }
    var updating by remember { mutableStateOf(false) }
    var testing by remember { mutableStateOf("") }
    val results = remember { mutableStateMapOf<String, String>() }
    val scope = rememberCoroutineScope()
    var testJob by remember { mutableStateOf<Job?>(null) }
    val groups = state.proxies.filter { it.members.isNotEmpty() }
    val selected = state.proxies.firstOrNull { it.selected }
    val updated = repository.uiPreferences.getLong("profileUpdated", 0)
    val members = groups.firstOrNull { it.name == group }?.members
    val visible = state.proxies.filter { it.name.contains(query, true) && (!favoritesOnly || it.name in favorites) && (members == null || it.name in members) }
    fun test(nodes: List<ProxyItem>) {
        if (testJob != null) return
        testJob = scope.launch(start = kotlinx.coroutines.CoroutineStart.LAZY) {
            try { for (node in nodes) {
                testing = node.name
                try { val result = repository.request("testProxy", json("name" to node.name)); results[node.name] = delayLabel(result.optLong("delay", -1)) }
                catch (e: Exception) { if (e is kotlinx.coroutines.CancellationException) throw e; results[node.name] = "失败" }
                repository.refresh()
            } } catch (e: Exception) { if (e is kotlinx.coroutines.CancellationException) throw e; repository.reportError(e.message ?: "测速结果刷新失败") } finally { testing = ""; testJob = null }
        }
        testJob?.start()
    }
    fun LazyListScope.connectionContent() {
        item { Card(colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surface)) {
            Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Icon(Icons.Outlined.Language, null, tint = MaterialTheme.colorScheme.primary, modifier = Modifier.size(28.dp))
                    Column(Modifier.weight(1f).padding(horizontal = 12.dp)) { Text("代理上网", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold); Text(if (state.proxies.isEmpty()) "尚未导入配置" else label, Modifier.clickable { renameDraft = label; rename = true }, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant); if (updated > 0) Text("更新于 " + java.time.format.DateTimeFormatter.ofPattern("MM-dd HH:mm").withZone(java.time.ZoneId.systemDefault()).format(java.time.Instant.ofEpochMilli(updated)), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
                    Box {
                        IconButton(onClick = { profileMenu = true }) { Icon(Icons.Outlined.MoreVert, "订阅与应用设置") }
                        DropdownMenu(profileMenu, { profileMenu = false }) {
                            DropdownMenuItem(text = { Text("导入配置") }, onClick = { profileMenu = false; importing = true })
                            if (state.profileURL.isNotEmpty()) DropdownMenuItem(text = { Text(if (updating) "更新中…" else "更新订阅") }, enabled = !updating, onClick = { profileMenu = false; scope.launch { updating = true; try { repository.safely { repository.request("importProfile", json("url" to state.profileURL)); repository.uiPreferences.edit().putLong("profileUpdated", System.currentTimeMillis()).apply(); repository.refresh() } } finally { updating = false } } })
                            DropdownMenuItem(text = { Text("应用绕过") }, onClick = { profileMenu = false; bypass = true })
                        }
                    }
                    Switch(state.proxyEnabled && state.vpnRunning, { network(state.privateAccess, it) }, enabled = state.initialized)
                }
                SegmentedTabs(listOf("规则", "全局", "直连"), listOf("rule", "global", "direct").indexOf(state.mode).coerceAtLeast(0)) { index -> repository.command("mode", json("mode" to listOf("rule", "global", "direct")[index])) }
                HorizontalDivider()
                Row(Modifier.fillMaxWidth().clickable(enabled = selected != null) { query = selected?.name.orEmpty(); group = ""; favoritesOnly = false }, verticalAlignment = Alignment.CenterVertically) { Text("当前节点", Modifier.weight(1f), style = MaterialTheme.typography.labelMedium); Icon(Icons.Outlined.ChevronRight, "定位当前节点") }
                Row(verticalAlignment = Alignment.CenterVertically) { Text(selected?.name ?: "尚未选择", Modifier.weight(1f), fontWeight = FontWeight.SemiBold, maxLines = 2, overflow = TextOverflow.Ellipsis); if (selected != null) Text(delayLabel(selected.delay), style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.primary) }
                Text("设备消息与文件保持连接", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)

            }
        } }
    }
    val accessCard: @Composable () -> Unit = { Surface(shape = MaterialTheme.shapes.medium, color = MaterialTheme.colorScheme.surface, onClick = { privateOptions = true }) {
            Row(Modifier.fillMaxWidth().padding(14.dp), verticalAlignment = Alignment.CenterVertically) {
                Icon(Icons.Outlined.Devices, null, tint = MaterialTheme.colorScheme.primary)
                Column(Modifier.weight(1f).padding(horizontal = 12.dp)) { Text("其他 App 访问设备", fontWeight = FontWeight.SemiBold); Text(if (state.vpnRunning && state.proxyEnabled) "已随代理启用" else if (state.vpnRunning && state.privateAccess) "已启用私网访问" else "未启用 · 不影响消息和文件", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
                Icon(Icons.Outlined.ChevronRight, null)
            }
        }
    }
    fun LazyListScope.nodeContent() {
        item { Row(verticalAlignment = Alignment.CenterVertically) {
            OutlinedTextField(query, { query = it }, Modifier.weight(1f), placeholder = { Text("搜索节点") }, leadingIcon = { Icon(Icons.Outlined.Search, null) }, singleLine = true, shape = MaterialTheme.shapes.medium)
            if (groups.isNotEmpty()) Box {
                IconButton(onClick = { groupMenu = true }) { Icon(Icons.Outlined.FilterList, if (group.isEmpty()) "筛选策略组" else "当前策略组：$group", tint = if (group.isEmpty()) MaterialTheme.colorScheme.onSurfaceVariant else MaterialTheme.colorScheme.primary) }
                DropdownMenu(groupMenu, { groupMenu = false }) {
                    DropdownMenuItem(text = { Text("所有节点与策略组") }, onClick = { group = ""; groupMenu = false })
                    groups.forEach { item -> DropdownMenuItem(text = { Text(item.name) }, onClick = { group = item.name; groupMenu = false }) }
                }
            }
        } }
        if (group.isNotEmpty()) item { Text("策略组：$group", style = MaterialTheme.typography.labelMedium) }
        item { Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            FilterChip(favoritesOnly, { favoritesOnly = true }, label = { Text("收藏") }); FilterChip(!favoritesOnly, { favoritesOnly = false }, label = { Text("全部") }); Spacer(Modifier.weight(1f))
            TextButton(enabled = testing.isNotEmpty() || (state.vpnRunning && visible.isNotEmpty()), onClick = { if (testing.isNotEmpty()) testJob?.cancel() else test(visible) }) { Text(if (testing.isNotEmpty()) "停止测速" else "测速") }
        } }
        if (!state.vpnRunning && state.proxies.isNotEmpty()) item { Text("可先选择节点；测速需要开启代理。", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
        if (visible.isEmpty()) item { EmptyCard("没有匹配的节点", if (state.proxies.isEmpty()) "导入 Clash 订阅或 YAML 配置后即可选择。" else "调整搜索条件，或切换到全部节点。") }
        items(visible, key = { it.name }) { proxy ->
            Surface(onClick = { repository.command("selectProxy", json("name" to proxy.name)) }, shape = MaterialTheme.shapes.medium, color = if (proxy.selected) MaterialTheme.colorScheme.secondaryContainer else MaterialTheme.colorScheme.surface) {
                Row(Modifier.fillMaxWidth().padding(start = 4.dp, end = 4.dp, top = 6.dp, bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                    RadioButton(proxy.selected, null)
                    Column(Modifier.weight(1f)) { Text(proxy.name, maxLines = 2, overflow = TextOverflow.Ellipsis, fontWeight = FontWeight.Medium); Text(if (proxy.members.isNotEmpty()) "策略组 · ${proxy.members.size} 个节点" else proxy.type, style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
                    TextButton(enabled = state.vpnRunning && testing.isEmpty(), onClick = { test(listOf(proxy)) }) { Text(if (testing == proxy.name) "测速中" else results[proxy.name] ?: delayLabel(proxy.delay), style = MaterialTheme.typography.labelMedium) }
                    IconButton(onClick = { favorites = if (proxy.name in favorites) favorites - proxy.name else favorites + proxy.name; repository.uiPreferences.edit().putStringSet("favorites", favorites).apply() }) { Icon(if (proxy.name in favorites) Icons.Outlined.Star else Icons.Outlined.StarBorder, if (proxy.name in favorites) "取消收藏" else "收藏节点", Modifier.size(20.dp), tint = MaterialTheme.colorScheme.primary) }
                }
            }
        }
    }
    BoxWithConstraints(Modifier.fillMaxSize()) {
        if (maxWidth >= 760.dp) Row(Modifier.fillMaxSize(), horizontalArrangement = Arrangement.spacedBy(20.dp)) {
            LazyColumn(Modifier.weight(.42f), verticalArrangement = Arrangement.spacedBy(12.dp), contentPadding = PaddingValues(vertical = 8.dp)) { connectionContent(); item { accessCard() } }
            LazyColumn(Modifier.weight(.58f), verticalArrangement = Arrangement.spacedBy(10.dp), contentPadding = PaddingValues(vertical = 8.dp)) { nodeContent() }
        } else Column(Modifier.fillMaxSize(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            LazyColumn(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(8.dp), contentPadding = PaddingValues(top = 8.dp, bottom = 8.dp)) { connectionContent(); nodeContent() }
            Box(Modifier.padding(bottom = 8.dp)) { accessCard() }
        }
    }
    if (importing) ImportDialog(repository) { importing = false }
    if (bypass) BypassDialog(state, repository) { bypass = false }
    if (rename) AlertDialog(onDismissRequest = { rename = false }, title = { Text("订阅名称") }, text = { OutlinedTextField(renameDraft, { renameDraft = it.take(60) }, singleLine = true, label = { Text("例如：我的订阅") }) }, confirmButton = { TextButton(onClick = { label = renameDraft.trim().ifBlank { "当前订阅" }; repository.uiPreferences.edit().putString("profileName", label).apply(); rename = false }) { Text("保存") } })
    if (privateOptions) AlertDialog(onDismissRequest = { privateOptions = false }, title = { Text("其他 App 访问设备") }, text = { Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Text("让浏览器、SSH 等 App 访问已配对设备，需要系统 VPN。消息与文件使用独立设备连接。")
        if (state.proxyEnabled && state.vpnRunning) Text("当前私网访问已随代理启用。以下开关控制关闭代理后是否仍保留私网 VPN。")
        Row(verticalAlignment = Alignment.CenterVertically) { Text("代理关闭后仍可访问", Modifier.weight(1f)); Switch(state.privateAccess, { network(it, state.proxyEnabled) }) }
        if (!state.vpnRunning && NetworkPolicy.desiredVPN(state.privateAccess, state.proxyEnabled)) TextButton(onClick = { network(state.privateAccess, state.proxyEnabled) }) { Text("恢复系统 VPN") }
    } }, confirmButton = { TextButton(onClick = { privateOptions = false }) { Text("完成") } })
}
