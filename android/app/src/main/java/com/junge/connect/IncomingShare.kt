package com.junge.connect

import android.content.Intent
import android.net.Uri
import android.os.Build
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.io.FileOutputStream
import java.util.UUID

/** App-private staging makes temporary share grants durable without loading a file into RAM. */
data class SharedFile(val source: String, val name: String)
data class PendingShare(val id: String, val text: String, val files: List<SharedFile>) {
    fun toJSON() = json("id" to id, "text" to text, "files" to JSONArray(files.map { json("source" to it.source, "name" to it.name) }))
    companion object { fun parse(j: JSONObject) = PendingShare(j.getString("id"), j.optString("text"), j.optJSONArray("files").objects().map { SharedFile(it.getString("source"), it.getString("name")) }) }
}

fun Intent.sharedURIs(): List<Uri> {
    val streams: List<Uri> = if (action == Intent.ACTION_SEND_MULTIPLE) {
        if (Build.VERSION.SDK_INT >= 33) getParcelableArrayListExtra(Intent.EXTRA_STREAM, Uri::class.java).orEmpty()
        else @Suppress("DEPRECATION") (getParcelableArrayListExtra<android.os.Parcelable>(Intent.EXTRA_STREAM).orEmpty().filterIsInstance<Uri>())
    } else {
        val single = if (Build.VERSION.SDK_INT >= 33) getParcelableExtra(Intent.EXTRA_STREAM, Uri::class.java) else @Suppress("DEPRECATION") (getParcelableExtra<android.os.Parcelable>(Intent.EXTRA_STREAM) as? Uri)
        listOfNotNull(single)
    }
    return (streams + (clipData?.let { clip -> (0 until clip.itemCount).mapNotNull { clip.getItemAt(it).uri } } ?: emptyList())).distinct()
}

suspend fun copyShareFile(context: android.content.Context, uri: Uri, directory: File): SharedFile = withContext(Dispatchers.IO) {
    require(uri.scheme == "content") { "分享文件必须来自系统文件提供方。" }
    val name = context.contentResolver.query(uri, arrayOf(android.provider.OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c -> if (c.moveToFirst()) c.getString(0) else null } ?: "文件"
    requireTransferName(name)
    check(directory.isDirectory || directory.mkdirs()) { "无法创建待发送目录，请检查手机可用空间。" }
    val target = File(directory, UUID.randomUUID().toString())
    val temporary = File(directory, "${target.name}.partial")
    try {
        context.contentResolver.openInputStream(uri)?.use { input ->
            FileOutputStream(temporary).use { output ->
                val buffer = ByteArray(1024 * 1024)
                while (true) { currentCoroutineContext().ensureActive(); val count = input.read(buffer); if (count < 0) break; output.write(buffer, 0, count) }
                output.fd.sync()
            }
        } ?: error("文件提供方无法打开 $name")
        check(temporary.renameTo(target)) { "无法保存待发送文件，请检查手机可用空间。" }
        SharedFile(target.absolutePath, name)
    } finally { temporary.delete() }
}
