package com.junge.connect

import android.app.Application
import android.content.Intent
import android.content.Context
import android.net.ConnectivityManager
import android.net.Uri
import android.provider.DocumentsContract
import android.provider.OpenableColumns
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.flowOn
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import mobile.Engine
import mobile.Mobile
import mobile.Platform
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.net.InetSocketAddress
import java.util.concurrent.atomic.AtomicReference

class JunGoApplication : Application() {
    val repository: NativeRepository by lazy { NativeRepository(this) }
}

data class Peer(val id: String, val name: String, val ip: String, val hostname: String, val path: String)
data class ProxyItem(val name: String, val type: String, val selected: Boolean, val delay: Long, val members: List<String> = emptyList())
data class Transfer(
    val id: String, val name: String, val direction: String, val size: Long,
    val completed: Long, val status: String, val error: String, val source: String = "",
    val deviceId: String = "", val destination: String = "", val chatMessageId: String = "", val created: String = "",
    val shareId: String = "", val path: String = ""
) {
    val active get() = status in setOf("queued", "hashing", "running", "waiting")
}
data class RemoteShare(val id: String, val name: String, val readOnly: Boolean)
data class RemoteEntry(val name: String, val path: String, val directory: Boolean, val size: Long)
data class Snapshot(
    val paired: Boolean = false,
    val deviceName: String = "",
    val meshEnabled: Boolean = false,
    val meshRunning: Boolean = false,
    // UI/desired private-network VPN access. Deliberately separate from the
    // native device connection (mesh) so the VPN switch can never gate chat.
    val privateAccess: Boolean = false,
    // The user's explicit pause of the private device connection.
    val connectionsPaused: Boolean = false,
    val proxyEnabled: Boolean = false,
    val vpnRunning: Boolean = false,
    val mode: String = "rule",
    val peers: List<Peer> = emptyList(),
    val proxies: List<ProxyItem> = emptyList(),
    val transfers: List<Transfer> = emptyList(),
    val profileURL: String = "",
    val bypassUIDs: List<Int> = emptyList(),
    val engineError: String = "",
    val activeIncoming: Int = 0,
    val initialized: Boolean = false
) {
    companion object {
        fun parse(j: JSONObject) = Snapshot(
            paired = j.optBoolean("paired"), deviceName = j.optJSONObject("device")?.optString("name").orEmpty(),
            meshEnabled = j.optBoolean("meshEnabled"), meshRunning = j.optBoolean("meshRunning"), proxyEnabled = j.optBoolean("proxyEnabled"),
            vpnRunning = j.optBoolean("vpnRunning"), mode = j.optString("mode", "rule"),
            peers = j.optJSONArray("peers").objects().map { Peer(it.optString("id"), it.optString("name"), it.optString("ip"), it.optString("hostname"), it.optString("path", "unavailable")) },
            proxies = j.optJSONArray("proxies").objects().map { ProxyItem(it.optString("name"), it.optString("type"), it.optBoolean("selected"), it.optLong("delay", -1), it.optJSONArray("members").strings()) },
            transfers = j.optJSONArray("transfers").objects().map { Transfer(it.optString("id"), it.optString("name"), it.optString("direction"), it.optLong("size"), it.optLong("completed"), it.optString("status"), it.optString("error"), it.optString("source"), it.optString("deviceId"), it.optString("destination"), it.optString("chatMessageId"), it.optString("created"), it.optString("shareId"), it.optString("path")) },
            profileURL = j.optString("profileURL"), bypassUIDs = (j.optJSONArray("bypassUIDs") ?: JSONArray()).let { a -> (0 until a.length()).map { a.optInt(it) } }, engineError = j.optString("error"), activeIncoming = j.optInt("activeIncoming"), initialized = true
        )
    }
}

fun JSONArray?.objects(): List<JSONObject> = if (this == null) emptyList() else (0 until length()).mapNotNull { optJSONObject(it) }
fun JSONArray?.strings(): List<String> = if (this == null) emptyList() else (0 until length()).map { optString(it) }
fun json(vararg values: Pair<String, Any?>) = JSONObject().apply { values.forEach { (key, value) -> put(key, value ?: JSONObject.NULL) } }

// Process-owned engine. Activities only observe it; folding/rotation never
// recreates the VPN, native connection or persistent transfer queue.
class NativeRepository(private val app: Application) {
    val uiPreferences = app.getSharedPreferences("interface-v2", Context.MODE_PRIVATE)
    private val mutableMessages = MutableStateFlow<List<ChatMessage>>(emptyList())
    val messages: StateFlow<List<ChatMessage>> = mutableMessages.asStateFlow()
    private val mutableRead = MutableStateFlow(uiPreferences.getStringSet("read", emptySet()).orEmpty().toSet())
    val readMessages: StateFlow<Set<String>> = mutableRead.asStateFlow()
    private val mutableConversations = MutableStateFlow(ConversationIndex())
    val conversations: StateFlow<ConversationIndex> = mutableConversations.asStateFlow()
    private val mutableTransfers = MutableStateFlow<List<Transfer>>(emptyList())
    val displayedTransfers: StateFlow<List<Transfer>> = mutableTransfers.asStateFlow()
    private val mutableRates = MutableStateFlow<Map<String, Long>>(emptyMap())
    val rates: StateFlow<Map<String, Long>> = mutableRates.asStateFlow()
    private val rateEstimator = TransferRates()
    private val mutableKnownPeers = MutableStateFlow(runCatching { JSONArray(uiPreferences.getString("peers", "[]")).objects().map { Peer(it.getString("id"), it.getString("name"), it.optString("ip"), it.optString("hostname"), "unavailable") } }.getOrDefault(emptyList()))
    val knownPeers: StateFlow<List<Peer>> = mutableKnownPeers.asStateFlow()

    fun markRead(device: String) {
        val ids = mutableRead.value + mutableConversations.value.byDevice[device].orEmpty().filter { it.direction == "incoming" && it.status in setOf("received", "complete") }.map { it.id }
        if (ids != mutableRead.value) { mutableRead.value = ids; uiPreferences.edit().putStringSet("read", ids).apply() }
    }
    private val composer = ChatComposer(uiPreferences.all.filterKeys { it.startsWith("draft:") }
        .mapKeys { it.key.removePrefix("draft:") }.mapValues { it.value as? String ?: "" }) { device, text ->
        uiPreferences.edit().putString("draft:$device", text).apply()
    }
    val drafts = composer.drafts
    val sendingMessages = composer.sending
    fun draft(device: String): String = drafts.value[device].orEmpty()
    fun saveDraft(device: String, text: String) = composer.save(device, text)
    fun sendText(device: String) {
        scope.launch { safely {
            if (composer.send(device) { text -> request("chatSendText", json("deviceId" to device, "text" to text)) }) refresh()
        } }
    }
    val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    val unreadCounts: StateFlow<Map<String, Int>> = combine(conversations, readMessages) { index, read -> index.unread(read) }
        .flowOn(Dispatchers.Default).stateIn(scope, SharingStarted.Eagerly, emptyMap())
    private val mutex = Mutex()
    private val refreshMutex = Mutex()
    // Guarded by refreshMutex and advanced only with the published messages.
    private var chatVersion = ""
    private var engine: Engine? = null
    private var nativeVpnOwner: JunGoVpnService? = null
    private val vpn = AtomicReference<JunGoVpnService?>(null)
    private val mutableState = MutableStateFlow(Snapshot())
    val state: StateFlow<Snapshot> = mutableState.asStateFlow()
    private val mutableError = MutableStateFlow<String?>(null)
    val error: StateFlow<String?> = mutableError.asStateFlow()
    private val mutablePreparations = MutableStateFlow(0)
    val preparations: StateFlow<Int> = mutablePreparations.asStateFlow()
    private val preparationJobs = mutableSetOf<Job>()
    var transferTimeoutDraining: Boolean = false
        private set
    private val observers = mutableSetOf<String>()
    private var polling: Job? = null
    private val shareMutex = Mutex()
    private val shareDirectory = File(app.noBackupFilesDir, "share-outbox")
    private val shareManifest = File(app.noBackupFilesDir, "pending-share.json")
    private val mutablePendingShare = MutableStateFlow(runCatching { PendingShare.parse(JSONObject(shareManifest.readText())) }.getOrNull())
    val pendingShare: StateFlow<PendingShare?> = mutablePendingShare.asStateFlow()

    private suspend fun setPendingShare(share: PendingShare?) = withContext(Dispatchers.IO) {
        if (share == null) shareManifest.delete() else {
            val temp = File(shareManifest.parentFile, "pending-share.tmp")
            java.io.FileOutputStream(temp).use { out -> out.write(share.toJSON().toString().toByteArray()); out.fd.sync() }
            check(temp.renameTo(shareManifest)) { "保存待分享记录失败" }
        }
        mutablePendingShare.value = share
    }

    fun receiveShare(intent: Intent) {
        if (intent.action !in setOf(Intent.ACTION_SEND, Intent.ACTION_SEND_MULTIPLE)) return
        val payload = runCatching { intent.sharedURIs() to intent.getCharSequenceExtra(Intent.EXTRA_TEXT)?.toString().orEmpty() }
            .getOrElse { reportError("无法读取分享内容，请从来源应用重新分享。"); return }
        val (uris, text) = payload
        prepareTransfers(app) { shareMutex.withLock {
            require(uris.size <= 1000 && text.unicodeLength() <= SHARED_TEXT_CODEPOINT_LIMIT) { "一次最多分享 1000 个文件或 16384 字文字；较长文字会自动分段。" }
            val files = mutableListOf<SharedFile>()
            try {
                for (uri in uris) {
                    val persistent = runCatching { persistUri(uri); true }.getOrDefault(false)
                    files += if (persistent) {
                        val name = withContext(Dispatchers.IO) { JSONObject(platform.uriInfo(uri.toString())).optString("name", "文件") }
                        requireTransferName(name)
                        SharedFile(uri.toString(), name)
                    } else copyShareFile(app, uri, shareDirectory)
                }
                val previous = mutablePendingShare.value
                val joinedText = listOfNotNull(previous?.text?.takeIf { it.isNotBlank() }, text.takeIf { it.isNotBlank() }).joinToString("\n")
                require(previous?.files.orEmpty().size + files.size <= 1000) { "待分享文件超过 1000 个，请先发送已有内容。" }
                require(joinedText.unicodeLength() <= SHARED_TEXT_CODEPOINT_LIMIT) { "待分享文字超过 16384 字，请先发送已有内容。" }
                setPendingShare(PendingShare(java.util.UUID.randomUUID().toString(), joinedText, previous?.files.orEmpty() + files))
            } catch (e: Exception) {
                files.filter { it.source.startsWith(shareDirectory.absolutePath + "/") }.forEach { File(it.source).delete() }
                throw e
            }
        } }
    }

    fun sendPendingShare(context: Context, deviceId: String) {
        val pending = mutablePendingShare.value ?: return
        prepareTransfers(context) { shareMutex.withLock {
            if (mutablePendingShare.value?.id != pending.id) return@withLock
            // Persist remaining items after each accepted native enqueue to avoid
            // replaying previously queued files when a later source fails.
            var remaining = pending
            while (remaining.text.isNotEmpty()) {
                val (chunk, rest) = takeMessageChunk(remaining.text)
                kotlinx.coroutines.currentCoroutineContext().ensureActive()
                withContext(NonCancellable) {
                    if (chunk.isNotBlank()) request("chatSendText", json("deviceId" to deviceId, "text" to chunk))
                    remaining = remaining.copy(text = rest); setPendingShare(remaining)
                }
            }
            while (remaining.files.isNotEmpty()) {
                val file = remaining.files.first()
                kotlinx.coroutines.currentCoroutineContext().ensureActive()
                withContext(NonCancellable) {
                    request("chatSendFile", json("deviceId" to deviceId, "source" to file.source, "name" to file.name))
                    remaining = remaining.copy(files = remaining.files.drop(1)); setPendingShare(remaining)
                }
                refresh()
            }
            if (mutablePendingShare.value?.id == pending.id) setPendingShare(null)
        } }
    }

    fun discardPendingShare() {
        scope.launch { safely { shareMutex.withLock {
            val pending = mutablePendingShare.value
            setPendingShare(null)
            withContext(Dispatchers.IO) { pending?.files?.filter { it.source.startsWith(shareDirectory.absolutePath + "/") }?.forEach { File(it.source).delete() } }
        } } }
    }

    private val platform = object : Platform {
        override fun protect(fd: Long): Boolean = vpn.get()?.protect(fd.toInt()) ?: !state.value.vpnRunning

        override fun ownerUID(protocol: Long, localIP: String, localPort: Long, remoteIP: String, remotePort: Long): Long =
            runCatching {
                app.getSystemService(ConnectivityManager::class.java).getConnectionOwnerUid(
                    protocol.toInt(), InetSocketAddress(localIP, localPort.toInt()), InetSocketAddress(remoteIP, remotePort.toInt())
                ).toLong()
            }.getOrDefault(-1)

        override fun openURI(uri: String, mode: String): Long = runCatching {
            require(mode == "r" || mode == "rw")
            app.contentResolver.openFileDescriptor(Uri.parse(uri), mode)?.use { it.detachFd().toLong() } ?: -1
        }.getOrDefault(-1)

        override fun uriInfo(uri: String): String = runCatching {
            val result = json("name" to "file", "size" to -1L, "modified" to 0L)
            app.contentResolver.query(Uri.parse(uri), null, null, null, null)?.use { cursor ->
                if (cursor.moveToFirst()) {
                    cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME).takeIf { it >= 0 }?.let { result.put("name", cursor.getString(it)) }
                    cursor.getColumnIndex(OpenableColumns.SIZE).takeIf { it >= 0 }?.let { if (!cursor.isNull(it)) result.put("size", cursor.getLong(it)) }
                    cursor.getColumnIndex(DocumentsContract.Document.COLUMN_LAST_MODIFIED).takeIf { it >= 0 }?.let { result.put("modified", cursor.getLong(it)) }
                }
            }
            result.toString()
        }.getOrElse { json("error" to (it.message ?: "文件信息不可用")).toString() }
    }

    private fun getEngine(): Engine = engine ?: Mobile.newEngine(File(app.noBackupFilesDir, "engine").apply { mkdirs() }.absolutePath, platform).also { engine = it }

    private fun rawRequest(method: String, params: JSONObject = JSONObject()): JSONObject =
        JSONObject(getEngine().request(json("method" to method, "params" to params).toString()))

    suspend fun request(method: String, params: JSONObject = JSONObject()): JSONObject = withContext(Dispatchers.IO) {
        // Go owns request-level synchronization. Only construction and VPN FD
        // ownership use this lock: a slow peer must not block pause/cancel/state.
        val native = mutex.withLock { getEngine() }
        kotlinx.coroutines.currentCoroutineContext().ensureActive()
        JSONObject(native.request(json("method" to method, "params" to params).toString()))
    }

    /** Desired private-network VPN access, with the pre-release "mesh" flag as the migration source. */
    fun privateAccessDesired(): Boolean {
        val preferences = app.getSharedPreferences("network", Context.MODE_PRIVATE)
        return preferences.getBoolean("privateAccess", preferences.getBoolean("mesh", false))
    }

    fun connectionsPaused(): Boolean = app.getSharedPreferences("network", Context.MODE_PRIVATE).getBoolean("connectionsPaused", false)

    /** Republishes the two preference-backed switches without a native round trip. */
    fun syncNetworkFlags() {
        mutableState.value = mutableState.value.copy(privateAccess = privateAccessDesired(), connectionsPaused = connectionsPaused())
    }

    /**
     * Starts the private device connection for a paired phone. Call this only
     * from a visible Activity or another explicit user action; the service is
     * never raised while the user has paused the connection.
     */
    fun ensureDeviceConnections(context: Context, resume: Boolean = false) {
        if (resume) {
            context.getSharedPreferences("network", Context.MODE_PRIVATE).edit().putBoolean("connectionsPaused", false).apply()
            syncNetworkFlags()
        }
        if (connectionsPaused()) return
        if (!state.value.paired) return
        runCatching { context.startForegroundService(Intent(context, DeviceConnectionService::class.java)) }
            .onFailure { reportError(it.message ?: "设备连接服务启动失败") }
    }

    suspend fun refresh() = refreshMutex.withLock {
        val previous = state.value
        val previousMessages = mutableMessages.value
        val (snapshot, update) = withContext(Dispatchers.Default) {
            val parsed = Snapshot.parse(request("state"))
            val snapshot = parsed.copy(
                peers = previous.peers.takeIf { it == parsed.peers } ?: parsed.peers,
                proxies = previous.proxies.takeIf { it == parsed.proxies } ?: parsed.proxies,
                transfers = previous.transfers.takeIf { it == parsed.transfers } ?: parsed.transfers)
            snapshot to decodeChatUpdate(request("chatList", json("version" to chatVersion)), chatVersion, previousMessages)
        }
        val chat = update.messages
        val (index, transfers) = withContext(Dispatchers.Default) {
            val index = if (chat === previousMessages) mutableConversations.value else ConversationIndex.build(chat, mutableConversations.value)
            val transfers = if (snapshot.transfers === previous.transfers && chat === previousMessages) mutableTransfers.value else unifiedTransfers(snapshot.transfers, chat)
            index to transfers
        }
        withContext(Dispatchers.Main.immediate) {
            mutableMessages.value = chat
            mutableConversations.value = index
            mutableTransfers.value = transfers
            val peerIDs = snapshot.peers.map { it.id }.toSet()
            val known = (snapshot.peers + mutableKnownPeers.value.filter { it.id !in peerIDs }.map { it.copy(path = "unavailable") }).distinctBy { it.id }
            if (known != mutableKnownPeers.value) {
                mutableKnownPeers.value = known
                uiPreferences.edit().putString("peers", JSONArray(known.map { json("id" to it.id, "name" to it.name, "ip" to it.ip, "hostname" to it.hostname) }).toString()).apply()
            }
            mutableRates.value = rateEstimator.update(transfers, android.os.SystemClock.elapsedRealtime())
            mutableState.value = snapshot.copy(privateAccess = privateAccessDesired(), connectionsPaused = connectionsPaused())
            chatVersion = update.version
        }
        if (snapshot.transfers !== previous.transfers) withContext(Dispatchers.IO) {
            val retained = snapshot.transfers.filter { it.status !in setOf("complete", "cancelled") }.map { it.source }.toSet() +
                mutablePendingShare.value?.files.orEmpty().map { it.source }
            snapshot.transfers.filter { it.status in setOf("complete", "cancelled") && it.source !in retained && it.source.startsWith(shareDirectory.absolutePath + "/") }
                .forEach { File(it.source).delete() }
        }
    }

    fun command(method: String, params: JSONObject = JSONObject(), onFinished: (() -> Unit)? = null, completed: ((JSONObject) -> Unit)? = null) {
        scope.launch {
            try { safely {
                val result = request(method, params)
                // Success is native acknowledgement; a subsequent read failure
                // must not encourage replaying an already accepted operation.
                completed?.invoke(result)
                refresh()
            } } finally { onFinished?.invoke() }
        }
    }

    suspend fun safely(block: suspend () -> Unit) {
        try { block() } catch (e: Exception) { if (e is kotlinx.coroutines.CancellationException) throw e; reportError(e.message ?: "操作失败") }
    }

    fun reportError(message: String) { mutableError.value = message }
    fun clearError() { mutableError.value = null }

    fun prepareTransfers(context: Context, block: suspend () -> Unit): Job {
        val job = scope.launch(start = kotlinx.coroutines.CoroutineStart.LAZY) {
            if (transferTimeoutDraining) { reportError("正在保存系统暂停的传输，请稍后再继续。"); return@launch }
            mutablePreparations.value++
            try {
                safely {
                    context.startForegroundService(Intent(context, TransferService::class.java))
                    block()
                }
            } finally { mutablePreparations.value-- }
        }
        preparationJobs.add(job)
        job.invokeOnCompletion { scope.launch { preparationJobs.remove(job) } }
        job.start()
        return job
    }

    fun cancelTransferPreparations(): List<Job> = preparationJobs.toList().also { jobs -> jobs.forEach { it.cancel() } }
    fun beginTransferTimeout(): List<Job> { transferTimeoutDraining = true; return cancelTransferPreparations() }
    fun finishTransferTimeout() { transferTimeoutDraining = false }

    fun observe(owner: String, active: Boolean) {
        val enteringForeground = active && owner.startsWith("activity:") && observers.none { it.startsWith("activity:") }
        if (active) observers.add(owner) else observers.remove(owner)
        if (enteringForeground) { polling?.cancel(); polling = null }
        if (observers.isNotEmpty() && polling?.isActive != true) {
            polling = scope.launch { while (observers.isNotEmpty()) {
                safely { refresh() }
                delay(refreshInterval(observers.any { it.startsWith("activity:") }, preparations.value, state.value))
            } }
        } else if (observers.isEmpty()) { polling?.cancel(); polling = null }
    }

    fun attachVpn(service: JunGoVpnService) { vpn.set(service) }

    suspend fun configureVpn(service: JunGoVpnService, privateAccess: Boolean, proxy: Boolean, createDescriptor: () -> Int) {
        val caller = kotlinx.coroutines.currentCoroutineContext()
        caller.ensureActive()
        withContext(Dispatchers.IO + NonCancellable) { mutex.withLock {
            caller.ensureActive()
            if (vpn.get() !== service) return@withLock
            val native = getEngine()
            rawRequest("network", json("proxy" to proxy))
            caller.ensureActive()
            if (vpn.get() !== service) return@withLock
            if (nativeVpnOwner !== service) {
                // Descriptor creation and ownership transfer are one lifecycle
                // operation, so an old service cannot close a newer VPN.
                native.startVPN(createDescriptor().toLong())
                nativeVpnOwner = service
            }
            app.getSharedPreferences("network", Context.MODE_PRIVATE).edit()
                .putBoolean("privateAccess", privateAccess).putBoolean("proxy", proxy).remove("mesh").apply()
        } }
        syncNetworkFlags()
    }

    suspend fun stopVpn(service: JunGoVpnService, detach: Boolean = false) {
        withContext(Dispatchers.IO + NonCancellable) { mutex.withLock {
            // Only the service that still owns the native VPN may tear it down,
            // so an old instance being destroyed never stops a newer one.
            if (vpn.get() !== service) return@withLock
            try {
                engine?.stopVPN()
                // The TUN carries proxy policy only: the private device
                // connection and every other preference stay untouched.
                rawRequest("network", json("proxy" to false))
            } finally { nativeVpnOwner = null; if (detach) vpn.compareAndSet(service, null) }
        } }
        refresh()
    }

    // Android keeps a VpnService bound while its TUN is open; stopService alone
    // cannot be relied on to reach onDestroy and release that descriptor.
    suspend fun stopActiveVpn() {
        vpn.get()?.let { stopVpn(it) }
    }

    fun persistUri(uri: Uri, write: Boolean = false) {
        val flags = Intent.FLAG_GRANT_READ_URI_PERMISSION or if (write) Intent.FLAG_GRANT_WRITE_URI_PERMISSION else 0
        try { app.contentResolver.takePersistableUriPermission(uri, flags) }
        catch (e: SecurityException) { throw IllegalArgumentException("此文件提供方不支持持久访问，请选择设备上的文件。", e) }
    }
}
