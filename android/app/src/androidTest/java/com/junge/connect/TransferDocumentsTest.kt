package com.junge.connect

import android.database.MatrixCursor
import android.net.Uri
import android.os.Bundle
import android.provider.DocumentsContract
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

class TransferDocumentsTest {
    private val folder = Uri.parse("content://com.junge.test/tree/root/document/root%2Fsub")
    private val columns = arrayOf(DocumentsContract.Document.COLUMN_DOCUMENT_ID, DocumentsContract.Document.COLUMN_DISPLAY_NAME, DocumentsContract.Document.COLUMN_MIME_TYPE)

    @Test fun childTraversalPreservesTheSelectedTreeAndUsesEachDocumentId() = runBlocking {
        val cursor = MatrixCursor(columns).apply {
            addRow(arrayOf("root/sub/child", "child", DocumentsContract.Document.MIME_TYPE_DIR))
            addRow(arrayOf("root/sub/file", "文件.bin", "application/octet-stream"))
        }
        cursor.use {
            val entries = readTransferDocuments(folder, it)
            assertEquals(2, entries.size)
            assertTrue(entries[0].directory)
            assertFalse(entries[1].directory)
            assertEquals("root", DocumentsContract.getTreeDocumentId(entries[0].uri))
            assertEquals("root/sub/child", DocumentsContract.getDocumentId(entries[0].uri))
            assertEquals("文件.bin", entries[1].name)
        }
    }

    @Test fun loadingOrInterruptedListingsNeverBecomeSuccessfulPartialTransfers() = runBlocking {
        val loading = MatrixCursor(columns).apply {
            addRow(arrayOf("root/sub/one", "one.bin", "application/octet-stream"))
            extras = Bundle().apply { putBoolean(DocumentsContract.EXTRA_LOADING, true) }
        }
        loading.use { assertTrue(runCatching { readTransferDocuments(folder, it) }.isFailure) }
        val interrupted = object : MatrixCursor(columns) {
            override fun onMove(oldPosition: Int, newPosition: Int): Boolean {
                if (newPosition == 1) throw IllegalStateException("provider disconnected")
                return true
            }
        }.apply {
            addRow(arrayOf("root/sub/one", "one.bin", "application/octet-stream"))
            addRow(arrayOf("root/sub/two", "two.bin", "application/octet-stream"))
        }
        interrupted.use { assertTrue(runCatching { readTransferDocuments(folder, it) }.isFailure) }
    }

    @Test fun malformedChildMetadataIsAnErrorInsteadOfAnOmittedFile() = runBlocking {
        val cursor = MatrixCursor(columns).apply { addRow(arrayOf("root/sub/file", "../escape", "application/octet-stream")) }
        cursor.use { assertTrue(runCatching { readTransferDocuments(folder, it) }.isFailure) }
        val missingType = MatrixCursor(columns).apply { addRow(arrayOf("root/sub/file", "file", null)) }
        missingType.use { assertTrue(runCatching { readTransferDocuments(folder, it) }.isFailure) }
    }
}
