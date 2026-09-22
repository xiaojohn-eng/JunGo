package com.junge.connect

import android.content.Intent
import androidx.activity.compose.BackHandler
import androidx.activity.compose.LocalActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.saveable.rememberSaveableStateHolder
import androidx.compose.ui.Modifier
import androidx.compose.ui.Alignment
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.window.layout.FoldingFeature
import androidx.window.layout.WindowInfoTracker
import androidx.window.layout.DisplayFeature
import kotlinx.coroutines.launch
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.map
import org.json.JSONArray

@Composable internal fun PairDialog(repository: NativeRepository, close: () -> Unit) {
    var server by rememberSaveable { mutableStateOf("") }; var fingerprint by rememberSaveable { mutableStateOf("") }; var code by rememberSaveable { mutableStateOf("") }; var name by rememberSaveable { mutableStateOf(android.os.Build.MODEL) }; var busy by remember { mutableStateOf(false) }; var pairError by remember { mutableStateOf<String?>(null) }
    val scope = repository.scope
    AlertDialog(onDismissRequest = { if (!busy) close() }, title = { Text("配对这台手机") }, text = {
        Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Text("输入自托管控制服务地址和一次性配对码。")
            pairError?.let { Text(it, color = MaterialTheme.colorScheme.error) }
            OutlinedTextField(server, { server = it }, modifier = Modifier.fillMaxWidth(), label = { Text("服务地址 · https://") }, singleLine = true)
            OutlinedTextField(fingerprint, { fingerprint = it }, modifier = Modifier.fillMaxWidth(), label = { Text("服务证书 SHA-256 指纹") }, supportingText = { Text("从你信任的服务端获取 64 位十六进制指纹。") }, minLines = 2)
            OutlinedTextField(code, { code = it }, modifier = Modifier.fillMaxWidth(), label = { Text("一次性配对码") }, singleLine = true)
            OutlinedTextField(name, { name = it }, modifier = Modifier.fillMaxWidth(), label = { Text("本机名称") }, singleLine = true)
        }
    }, confirmButton = { TextButton(enabled = !busy && server.trim().startsWith("https://") && fingerprint.trim().matches(Regex("[a-fA-F0-9]{64}")) && code.isNotBlank() && name.isNotBlank(), onClick = {
        busy = true; pairError = null; scope.launch { try { repository.request("pair", json("server" to server.trim(), "fingerprint" to fingerprint.trim(), "code" to code.trim(), "name" to name.trim())); close(); repository.refresh() } catch (e: Exception) { if (e is kotlinx.coroutines.CancellationException) throw e; pairError = e.message ?: "配对失败" } finally { busy = false } }
    }) { Text(if (busy) "配对中…" else "配对") } }, dismissButton = { TextButton(enabled = !busy, onClick = close) { Text("取消") } })
}


@Composable internal fun ImportDialog(repository: NativeRepository, close: () -> Unit) {
    var value by rememberSaveable { mutableStateOf("") }; var yaml by rememberSaveable { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    val context = LocalContext.current
    val picker = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocument()) { uri ->
        if (uri != null && !busy) { busy = true; repository.scope.launch { try { repository.safely {
            val data = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) {
                context.contentResolver.openInputStream(uri)?.use { input ->
                    val output = java.io.ByteArrayOutputStream()
                    val buffer = ByteArray(8192)
                    while (true) {
                        val count = input.read(buffer)
                        if (count < 0) break
                        require(output.size() + count <= 4 * 1024 * 1024) { "配置文件超过 4 MB" }
                        output.write(buffer, 0, count)
                    }
                    output.toByteArray()
                } ?: error("无法读取配置文件")
            }
            require(data.size <= 4 * 1024 * 1024) { "配置文件超过 4 MB" }
            repository.request("importProfile", json("yaml" to data.toString(Charsets.UTF_8)))
            repository.uiPreferences.edit().putLong("profileUpdated", System.currentTimeMillis()).apply()
            close(); repository.refresh()
        } } finally { busy = false } } }
    }
    AlertDialog(onDismissRequest = { if (!busy) close() }, title = { Text("导入代理配置") }, text = { Column {
        Row { FilterChip(!yaml, { yaml = false }, label = { Text("订阅链接") }); Spacer(Modifier.width(8.dp)); FilterChip(yaml, { yaml = true }, label = { Text("配置文本") }) }
        OutlinedTextField(value, { value = it }, label = { Text(if (yaml) "Clash YAML" else "https:// 订阅地址") }, minLines = if (yaml) 6 else 2, maxLines = 10)
        TextButton(enabled = !busy, onClick = { picker.launch(arrayOf("application/yaml", "text/yaml", "text/plain", "application/octet-stream")) }) { Text("选择 YAML 文件") }
    } }, confirmButton = { TextButton(enabled = !busy && value.isNotBlank(), onClick = { busy = true; repository.command("importProfile", json((if (yaml) "yaml" else "url") to value.trim()), onFinished = { busy = false }) { repository.uiPreferences.edit().putLong("profileUpdated", System.currentTimeMillis()).apply(); close() } }) { Text(if (busy) "导入中…" else "导入") } }, dismissButton = { TextButton(enabled = !busy, onClick = close) { Text("取消") } })
}

@Composable internal fun BypassDialog(state: Snapshot, repository: NativeRepository, close: () -> Unit) {
    val context = LocalContext.current
    var apps by remember { mutableStateOf<List<Triple<String, String, Int>>>(emptyList()) }
    var selected by remember { mutableStateOf(state.bypassUIDs.toSet()) }
    LaunchedEffect(Unit) { apps = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) {
        context.packageManager.queryIntentActivities(Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER), 0)
            .map { Triple(it.loadLabel(context.packageManager).toString(), it.activityInfo.packageName, it.activityInfo.applicationInfo.uid) }
            .filter { it.second != context.packageName }.distinctBy { it.third }.sortedBy { it.first }
    } }
    AlertDialog(onDismissRequest = close, title = { Text("绕过公网代理") }, text = { Column {
        Text("选中的应用仍可以访问你的私网设备。")
        LazyColumn(Modifier.heightIn(max = 400.dp)) { items(apps, key = { it.second }) { app -> Row(Modifier.fillMaxWidth().clickable { selected = if (app.third in selected) selected - app.third else selected + app.third }.padding(vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
            Checkbox(checked = app.third in selected, onCheckedChange = { selected = if (it) selected + app.third else selected - app.third }); Text(app.first, Modifier.weight(1f))
        } } }
    } }, confirmButton = { TextButton(onClick = { repository.command("network", json("bypassUIDs" to JSONArray(selected.toList()))) { close() } }) { Text("保存") } }, dismissButton = { TextButton(onClick = close) { Text("取消") } })
}
