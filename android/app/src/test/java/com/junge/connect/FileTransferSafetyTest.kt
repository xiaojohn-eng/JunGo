package com.junge.connect

import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File

class FileTransferSafetyTest {
    @get:Rule val temporary = TemporaryFolder()

    @Test fun pickerResultKeepsOriginalDeviceShareAndPathAfterNavigationAndRestore() {
        val launched = RemoteFileTarget("mac", "photos", "旅行/原图", "照片.png").save()
        val currentPane = RemoteFileTarget("server", "backup", "其他目录")
        val restored = RemoteFileTarget.restore(launched)
        assertEquals("mac", restored.params().getString("deviceId"))
        assertEquals("photos", restored.params().getString("shareId"))
        assertEquals("旅行/原图", restored.params().getString("path"))
        assertEquals("照片.png", restored.name)
        assertNotEquals(currentPane, restored)
    }

    @Test fun aMissingPickerAddressCannotFallBackToAnotherDevice() {
        assertTrue(runCatching { RemoteFileTarget.restore("""{"deviceId":"","shareId":"files","path":""}""") }.isFailure)
        assertTrue(runCatching { RemoteFileTarget.restore("{}") }.isFailure)
    }

    @Test fun providerNamesCannotEscapeTargetOrExceedNativeUtf8Limit() {
        listOf("", " ", ".", "..", "../identity", "folder/file", "folder\\file", "bad\u0000name", "汉".repeat(86)).forEach {
            assertFalse("Unexpected accepted provider filename: $it", isTransferName(it))
        }
        assertTrue(isTransferName("汉".repeat(85)))
        assertTrue(isTransferName("我的文档 (2).zip"))
    }

    @Test fun pausedPreviewReusesExactDeviceShareAndSourceWithoutRestarting() {
        val root = temporary.newFolder("previews")
        val paused = previewTask(root, "paused")
        val params = RemoteFileTarget("mac", "share", "folder/file.bin").params()
        assertEquals(paused, reusablePreview(listOf(paused), root, "download", params, ""))
        assertNull(reusablePreview(listOf(paused.copy(deviceId = "server")), root, "download", params, ""))
        assertNull(reusablePreview(listOf(paused.copy(shareId = "other")), root, "download", params, ""))
        assertNull(reusablePreview(listOf(paused.copy(path = "other.bin")), root, "download", params, ""))
        assertNull(reusablePreview(listOf(paused.copy(status = "cancelled")), root, "download", params, ""))
    }

    @Test fun previewsNeverReuseUserDestinationsOrEscapedCachePaths() {
        val root = temporary.newFolder("previews")
        val params = RemoteFileTarget("mac", "share", "folder/file.bin").params()
        val original = previewTask(root, "running")
        listOf("content://provider/user-file", File(root.parentFile, "previews-other/file.bin").path, File(root, "../identity").path).forEach {
            assertNull(reusablePreview(listOf(original.copy(destination = it)), root, "download", params, ""))
        }
    }

    @Test fun completedRemotePreviewsAreRefetchedButImmutableInboxCopiesAreReused() {
        val root = temporary.newFolder("previews")
        val completed = previewTask(root, "complete")
        File(completed.destination).apply { parentFile!!.mkdirs(); writeText("verified") }
        assertNull(reusablePreview(listOf(completed), root, "download", RemoteFileTarget("mac", "share", "folder/file.bin").params(), ""))
        val inbox = completed.copy(direction = "save", source = "inbox://mac/message/file.bin")
        assertEquals(inbox, reusablePreview(listOf(inbox), root, "chatSaveFile", json("id" to "message"), "mac/message/file.bin"))
        File(completed.destination).delete()
        assertNull(reusablePreview(listOf(inbox), root, "chatSaveFile", json("id" to "message"), "mac/message/file.bin"))
    }

    private fun previewTask(root: File, status: String) = Transfer(
        "task", "file.bin", "download", 5L * 1024 * 1024 * 1024, 100, status, "",
        deviceId = "mac", destination = File(root, "request/file.bin").path, shareId = "share", path = "folder/file.bin"
    )
}
