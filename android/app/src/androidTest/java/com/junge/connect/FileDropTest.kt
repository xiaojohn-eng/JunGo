package com.junge.connect

import android.content.ClipData
import android.os.SystemClock
import android.view.InputDevice
import android.view.MotionEvent
import android.view.View
import android.widget.Button
import androidx.activity.ComponentActivity
import androidx.compose.foundation.layout.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.activity.compose.setContent
import android.content.Intent
import androidx.core.content.FileProvider
import androidx.test.platform.app.InstrumentationRegistry
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.*
import org.junit.Assume.assumeTrue
import org.junit.Test
import java.io.File
import java.util.concurrent.atomic.AtomicBoolean

/** Explicit opt-in: sends one 1 KiB fixture only to the device from our earlier QA round-trip. */
class FileDropTest {

    @Test fun nativeDragStagesAndDeliversAFile(): Unit = runBlocking {
        assumeTrue(InstrumentationRegistry.getArguments().getString("runLiveDrop") == "true")
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val context = instrumentation.targetContext
        val repository = (context.applicationContext as JunGoApplication).repository
        val history = repository.request("chatList").optJSONArray("messages").objects().map(ChatMessage::parse)
        val fixture = history.firstOrNull { it.name == "jungo-return-1mib.bin" && it.direction == "incoming" && it.status == "complete" }
        assertNotNull("Previously verified own-device fixture required", fixture)
        val activity = instrumentation.startActivitySync(Intent(context, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) as ComponentActivity
        repository.ensureDeviceConnections(activity)
        repository.refresh()
        val peer = repository.knownPeers.value.first { it.id == fixture!!.deviceId }
        val name = "jungo-v2-drop-${System.currentTimeMillis()}.bin"
        val source = File(context.cacheDir, "previews/drag-test/$name").apply { parentFile!!.mkdirs(); writeBytes(ByteArray(1024) { (it % 251).toByte() }) }
        val uri = FileProvider.getUriForFile(context, "${context.packageName}.files", source)
        val started = AtomicBoolean(false)
        var sourceView: Button? = null
        instrumentation.runOnMainSync { activity.setContent { JunGoTheme { Column(Modifier.fillMaxSize().padding(40.dp)) {
            AndroidView(modifier = Modifier.fillMaxWidth().height(90.dp), factory = { ctx -> Button(ctx).also { button ->
                sourceView = button; button.text = "长按拖入 1 KiB 测试文件"
                button.setOnLongClickListener { view ->
                    started.set(view.startDragAndDrop(ClipData.newUri(ctx.contentResolver, name, uri), View.DragShadowBuilder(view), null, View.DRAG_FLAG_GLOBAL or View.DRAG_FLAG_GLOBAL_URI_READ)); true
                }
            } })
            Spacer(Modifier.height(100.dp))
            FileDropZone(peer, repository) { }
        } } } }
        withTimeout(10000) {
            while (true) {
                var ready = false
                instrumentation.runOnMainSync { ready = (sourceView?.height ?: 0) > 0 && activity.findViewById<View>(android.R.id.content).findViewWithTag<View>("jungo-file-drop-target")?.height?.let { it > 0 } == true }
                if (ready) break
                delay(100)
            }
        }
        val from = IntArray(2); val to = IntArray(2)
        instrumentation.runOnMainSync {
            sourceView!!.getLocationOnScreen(from); from[0] += sourceView!!.width / 2; from[1] += sourceView!!.height / 2
            val target = activity.findViewById<View>(android.R.id.content).findViewWithTag<View>("jungo-file-drop-target")
            target.getLocationOnScreen(to); to[0] += target.width / 2; to[1] += target.height / 2
        }
        val downTime = SystemClock.uptimeMillis()
        fun motion(action: Int, x: Int, y: Int) {
            val event = MotionEvent.obtain(downTime, SystemClock.uptimeMillis(), action, x.toFloat(), y.toFloat(), 0)
            event.source = InputDevice.SOURCE_TOUCHSCREEN
            try { assertTrue(instrumentation.uiAutomation.injectInputEvent(event, true)) } finally { event.recycle() }
        }
        motion(MotionEvent.ACTION_DOWN, from[0], from[1]); delay(850)
        assertTrue("Native platform drag started", started.get())
        for (step in 1..8) { motion(MotionEvent.ACTION_MOVE, from[0] + (to[0]-from[0])*step/8, from[1] + (to[1]-from[1])*step/8); delay(80) }
        motion(MotionEvent.ACTION_UP, to[0], to[1])
        withTimeout(45000) {
            while (true) {
                repository.refresh()
                val sent = repository.messages.value.firstOrNull { it.deviceId == peer.id && it.name == name && it.direction == "outgoing" }
                check(sent?.status != "failed") { sent?.error.orEmpty() }
                if (sent?.status == "complete") { assertEquals(1024L, sent.size); assertEquals(1024L, sent.completed); break }
                delay(300)
            }
        }
        instrumentation.runOnMainSync { activity.finish() }
        source.delete()
        Unit
    }
}
