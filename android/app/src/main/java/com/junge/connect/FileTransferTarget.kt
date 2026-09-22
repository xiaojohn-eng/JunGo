package com.junge.connect

import org.json.JSONObject

/** Immutable launch-time address; the current pane may change while the system picker is open. */
internal data class RemoteFileTarget(val deviceId: String, val shareId: String, val path: String, val name: String = "") {
    fun params() = json("deviceId" to deviceId, "shareId" to shareId, "path" to path)
    fun save(): String = params().put("name", name).toString()
    companion object {
        fun restore(value: String): RemoteFileTarget {
            val data = JSONObject(value)
            val target = RemoteFileTarget(data.getString("deviceId"), data.getString("shareId"), data.getString("path"), data.optString("name"))
            require(target.deviceId.isNotBlank() && target.shareId.isNotBlank()) { "文件选择目标已失效" }
            return target
        }
    }
}

internal data class RemoteListing(val location: RemoteFileTarget, val entries: List<RemoteEntry>)
internal data class RemoteFilePreview(val location: RemoteFileTarget, val entry: RemoteEntry)

// Match the native protocol's UTF-8 filename limit before copying/staging large inputs.
internal fun isTransferName(name: String): Boolean = name.isNotBlank() && name !in setOf(".", "..") &&
    name.none { it == '/' || it == '\\' || it == '\u0000' } && name.toByteArray(Charsets.UTF_8).size <= 255

internal fun requireTransferName(name: String) {
    require(isTransferName(name)) { "文件名称无效或过长，请重命名后重试。" }
}
