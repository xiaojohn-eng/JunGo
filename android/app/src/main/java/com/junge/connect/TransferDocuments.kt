package com.junge.connect

import android.content.Context
import android.database.Cursor
import android.net.Uri
import android.provider.DocumentsContract
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.withContext

internal data class TransferDocument(val uri: Uri, val name: String, val directory: Boolean)

/** DocumentFile.listFiles silently returns partial results on provider errors; transfers must fail instead. */
internal suspend fun listTransferDocuments(context: Context, folder: Uri): List<TransferDocument> = withContext(Dispatchers.IO) {
    val children = DocumentsContract.buildChildDocumentsUriUsingTree(folder, DocumentsContract.getDocumentId(folder))
    val columns = arrayOf(DocumentsContract.Document.COLUMN_DOCUMENT_ID, DocumentsContract.Document.COLUMN_DISPLAY_NAME, DocumentsContract.Document.COLUMN_MIME_TYPE)
    context.contentResolver.query(children, columns, null, null, null)?.use { cursor ->
        readTransferDocuments(folder, cursor) { currentCoroutineContext().ensureActive() }
    } ?: error("文件提供方未能完整读取目录，请稍后重试。")
}

internal suspend fun readTransferDocuments(folder: Uri, cursor: Cursor, checkCancelled: suspend () -> Unit = {}): List<TransferDocument> {
    val id = cursor.getColumnIndexOrThrow(DocumentsContract.Document.COLUMN_DOCUMENT_ID)
    val name = cursor.getColumnIndexOrThrow(DocumentsContract.Document.COLUMN_DISPLAY_NAME)
    val mime = cursor.getColumnIndexOrThrow(DocumentsContract.Document.COLUMN_MIME_TYPE)
    val result = mutableListOf<TransferDocument>()
    while (cursor.moveToNext()) {
        checkCancelled()
        val documentId = cursor.getString(id) ?: error("文件提供方返回了无效文件标识。")
        val displayName = cursor.getString(name) ?: error("文件名称不可用。")
        val type = cursor.getString(mime)?.takeIf { it.isNotEmpty() } ?: error("文件类型不可用。")
        requireTransferName(displayName)
        result += TransferDocument(DocumentsContract.buildDocumentUriUsingTree(folder, documentId), displayName, type == DocumentsContract.Document.MIME_TYPE_DIR)
    }
    check(!cursor.extras.getBoolean(DocumentsContract.EXTRA_LOADING, false)) { "文件提供方仍在加载目录，请加载完成后重试。" }
    return result
}
