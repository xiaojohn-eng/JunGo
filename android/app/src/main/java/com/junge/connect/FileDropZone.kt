package com.junge.connect

import android.graphics.drawable.GradientDrawable
import android.view.DragEvent
import android.view.Gravity
import android.widget.TextView
import androidx.activity.compose.LocalActivity
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import java.io.File

/** Native drag permissions stay alive until temporary URI content is durably staged. */
@Composable internal fun FileDropZone(peer: Peer, repository: NativeRepository, pickFiles: () -> Unit) {
    val activity = LocalActivity.current
    val normal = MaterialTheme.colorScheme.onSurfaceVariant.toArgb()
    val accent = MaterialTheme.colorScheme.primary.toArgb()
    val outline = MaterialTheme.colorScheme.outlineVariant.toArgb()
    val active = MaterialTheme.colorScheme.secondaryContainer.toArgb()
    AndroidView(modifier = Modifier.fillMaxWidth().heightIn(min = 54.dp), factory = { context ->
        TextView(context).apply { tag = "jungo-file-drop-target"; gravity = Gravity.CENTER; textSize = 14f; isClickable = true; isFocusable = true }
    }, update = { view ->
        val density = view.resources.displayMetrics.density
        fun render(hover: Boolean) {
            view.text = if (hover) "松开发送给${peer.name}" else "拖入文件，发送给${peer.name}"
            view.contentDescription = view.text
            view.setTextColor(if (hover) accent else normal)
            view.setPadding((12*density).toInt(), (14*density).toInt(), (12*density).toInt(), (14*density).toInt())
            view.background = GradientDrawable().apply {
                cornerRadius = 12*density; setColor(if (hover) active else android.graphics.Color.TRANSPARENT)
                setStroke(density.toInt().coerceAtLeast(1), if (hover) accent else outline, 5*density, 4*density)
            }
        }
        render(false)
        view.setOnClickListener { pickFiles() }
        view.setOnDragListener { _, event ->
            when (event.action) {
                DragEvent.ACTION_DRAG_STARTED -> event.clipDescription != null
                DragEvent.ACTION_DRAG_ENTERED -> { render(true); true }
                DragEvent.ACTION_DRAG_EXITED, DragEvent.ACTION_DRAG_ENDED -> { render(false); true }
                DragEvent.ACTION_DROP -> {
                    render(false)
                    val uris = event.clipData?.let { clip -> (0 until clip.itemCount).mapNotNull { clip.getItemAt(it).uri }.distinct() }.orEmpty()
                    if (uris.isEmpty() || uris.any { it.scheme != "content" }) {
                        repository.reportError("请拖入文件；文件夹请通过附件中的文件夹入口选择。")
                        false
                    } else {
                        val permission = runCatching { activity?.requestDragAndDropPermissions(event) }
                        if (permission.isFailure) {
                            repository.reportError("无法取得拖入文件的读取权限，请从附件选择文件。")
                            return@setOnDragListener false
                        }
                        val grant = permission.getOrNull()
                        val recipient = peer.id
                        val job = repository.prepareTransfers(view.context) {
                            val directory = File(view.context.noBackupFilesDir, "share-outbox")
                            for (uri in uris) {
                                val file = copyShareFile(view.context, uri, directory)
                                repository.request("chatSendFile", json("deviceId" to recipient, "source" to file.source, "name" to file.name))
                                repository.refresh()
                            }
                        }
                        job.invokeOnCompletion { grant?.release() }
                        true
                    }
                }
                else -> true
            }
        }
    })
}
