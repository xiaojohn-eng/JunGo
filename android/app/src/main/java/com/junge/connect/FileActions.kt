package com.junge.connect

import android.content.ClipData
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.webkit.MimeTypeMap
import androidx.core.content.FileProvider
import androidx.documentfile.provider.DocumentFile
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.io.File
import java.util.UUID
import java.util.concurrent.ConcurrentHashMap
import java.util.zip.ZipEntry
import java.util.zip.ZipOutputStream

fun openFile(context: Context, value: String, name: String, repository: NativeRepository, share: Boolean = false) {
    runCatching {
        val uri = if (value.startsWith("content://")) Uri.parse(value) else {
            val file = File(value)
            require(file.isFile) { "文件已移动或不存在，请重新下载。" }
            FileProvider.getUriForFile(context, "${context.packageName}.files", file)
        }
        val type = MimeTypeMap.getSingleton().getMimeTypeFromExtension(name.substringAfterLast('.', "").lowercase()) ?: "application/octet-stream"
        val intent = if (share) Intent(Intent.ACTION_SEND).setType(type).putExtra(Intent.EXTRA_STREAM, uri) else Intent(Intent.ACTION_VIEW).setDataAndType(uri, type)
        intent.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        intent.clipData = ClipData.newRawUri(name, uri)
        context.startActivity(Intent.createChooser(intent, if (share) "分享文件" else "打开文件").addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }.onFailure { repository.reportError(it.message ?: "无法打开文件，请尝试另存后打开。") }
}

/** The native save/download performs validation; only its completed destination is exposed. */
private val previewRequests = ConcurrentHashMap.newKeySet<String>()

fun prepareFilePreview(context: Context, repository: NativeRepository, name: String, method: String, params: JSONObject, share: Boolean = false, inboxPath: String = "") {
    val arguments = JSONObject(params.toString())
    val key = if (method == "chatSaveFile") "save:${arguments.optString("id")}" else "download:${arguments.optString("deviceId")}:${arguments.optString("shareId")}:${arguments.optString("path")}"
    if (!previewRequests.add(key)) { repository.reportError("正在准备此文件，可在传输页查看进度。"); return }
    repository.prepareTransfers(context) {
        requireTransferName(name)
        repository.refresh()
        val root = File(context.cacheDir, "previews")
        val existing = withContext(Dispatchers.IO) { reusablePreview(repository.state.value.transfers, root, method, arguments, inboxPath) }
        val id: String
        val destination: File
        if (existing != null) {
            id = existing.id
            destination = File(existing.destination)
        } else {
            val folder = File(root, UUID.randomUUID().toString())
            withContext(Dispatchers.IO) { check(folder.mkdirs()) { "无法创建预览缓存，请检查可用空间。" } }
            destination = File(folder, name)
            id = repository.request(method, arguments.put("destination", destination.absolutePath)).getString("id")
        }
        repository.refresh()
        val current = repository.state.map { it.transfers.firstOrNull { task -> task.id == id }
            ?: error("预览任务不存在，请重新打开文件。") }
            .first { it.status in setOf("complete", "failed", "cancelled", "paused") }
        if (current.status == "complete") {
            val owner = context as? androidx.lifecycle.LifecycleOwner
            if (owner != null && !owner.lifecycle.currentState.isAtLeast(androidx.lifecycle.Lifecycle.State.STARTED)) repository.reportError("文件已准备好，可在传输页点打开。")
            else openFile(context, destination.absolutePath, name, repository, share)
        } else error(when (current.status) {
            "failed" -> "预览失败：${current.error}。可在传输页重试。"
            "paused" -> "预览已暂停，可在传输页继续。"
            else -> "预览已取消。"
        })
    }.invokeOnCompletion { previewRequests.remove(key) }
}

/** Paused/failed copies keep their breakpoint; clicking Open never creates a parallel copy. */
internal fun reusablePreview(tasks: List<Transfer>, previewRoot: File, method: String, params: JSONObject, inboxPath: String): Transfer? {
    val root = previewRoot.canonicalPath + File.separator
    return tasks.lastOrNull { task ->
        val sameSource = when (method) {
            "chatSaveFile" -> inboxPath.isNotBlank() && task.direction == "save" && task.source == "inbox://$inboxPath"
            "download" -> task.direction == "download" && task.deviceId == params.optString("deviceId") && task.shareId == params.optString("shareId") && task.path == params.optString("path")
            else -> false
        }
        sameSource && task.status != "cancelled" && task.destination.isNotBlank() &&
            !task.destination.startsWith("content://") && File(task.destination).canonicalPath.startsWith(root) &&
            // Remote shared files may have changed; only immutable inbox copies can reuse a completed preview.
            (task.status != "complete" || method == "chatSaveFile" && File(task.destination).isFile)
    }
}

fun openChatFile(context: Context, repository: NativeRepository, message: ChatMessage, share: Boolean = false) {
    val existing = repository.state.value.transfers.lastOrNull { it.direction == "save" && it.status == "complete" && it.source == "inbox://${message.path}" }
    if (existing != null && (existing.destination.startsWith("content://") || File(existing.destination).isFile)) openFile(context, existing.destination, message.name, repository, share)
    else prepareFilePreview(context, repository, message.name, "chatSaveFile", json("id" to message.id), share, message.path)
}

fun sendChatDocuments(context: Context, repository: NativeRepository, device: String, uris: List<Uri>) {
    if (uris.isEmpty()) return
    repository.prepareTransfers(context) {
        try { for (uri in uris) {
            currentCoroutineContext().ensureActive()
            repository.persistUri(uri)
            val name = withContext(Dispatchers.IO) { DocumentFile.fromSingleUri(context, uri)?.name } ?: "文件"
            requireTransferName(name)
            repository.request("chatSendFile", json("deviceId" to device, "source" to uri.toString(), "name" to name))
        } } finally { withContext(NonCancellable) { repository.refresh() } }
    }
}

fun sendChatFolder(context: Context, repository: NativeRepository, device: String, uri: Uri) {
    repository.prepareTransfers(context) {
        repository.persistUri(uri)
        val root = DocumentFile.fromTreeUri(context, uri) ?: error("无法读取文件夹")
        val directory = File(context.noBackupFilesDir, "share-outbox")
        val archive = File(directory, UUID.randomUUID().toString() + ".zip")
        val temporary = File(directory, archive.name + ".partial")
        val name = withContext(Dispatchers.IO) {
            require(root.isDirectory && root.canRead()) { "无法读取文件夹" }
            val archiveName = (root.name ?: "文件夹") + ".zip"
            requireTransferName(archiveName)
            check(directory.isDirectory || directory.mkdirs()) { "无法创建待发送目录，请检查手机可用空间。" }
            try {
                ZipOutputStream(temporary.outputStream().buffered()).use { output ->
                    val buffer = ByteArray(1024 * 1024)
                    suspend fun append(folder: Uri, prefix: String, depth: Int) {
                        require(depth < 64) { "文件夹层级过深" }
                        for (child in listTransferDocuments(context, folder)) {
                            currentCoroutineContext().ensureActive()
                            val part = child.name
                            requireTransferName(part)
                            val path = prefix + part
                            if (child.directory) { output.putNextEntry(ZipEntry("$path/")); output.closeEntry(); append(child.uri, "$path/", depth + 1) }
                            else {
                                output.putNextEntry(ZipEntry(path))
                                context.contentResolver.openInputStream(child.uri)?.use { input ->
                                    while (true) { currentCoroutineContext().ensureActive(); val count = input.read(buffer); if (count < 0) break; output.write(buffer, 0, count) }
                                } ?: error("无法读取 $part")
                                output.closeEntry()
                            }
                        }
                    }
                    append(root.uri, "", 0)
                }
                check(temporary.renameTo(archive)) { "无法保存打包文件，请检查可用空间。" }
                archiveName
            } finally { temporary.delete() }
        }
        repository.request("chatSendFile", json("deviceId" to device, "source" to archive.absolutePath, "name" to name))
        repository.refresh()
    }
}
