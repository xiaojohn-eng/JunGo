package com.junge.connect

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.content.SharedPreferences
import android.content.pm.ServiceInfo
import android.net.VpnService
import android.os.Build
import android.os.IBinder
import android.os.PowerManager
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.launch

internal fun Service.notification(channel: String, title: String, text: String): Notification {
    val manager = getSystemService(NotificationManager::class.java)
    manager.createNotificationChannel(NotificationChannel(channel, title, NotificationManager.IMPORTANCE_LOW))
    val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    return Notification.Builder(this, channel).setSmallIcon(R.drawable.ic_jungo).setContentTitle(title)
        .setContentText(text).setContentIntent(open).setOngoing(true).setOnlyAlertOnce(true).build()
}

// The system TUN carries proxy policy and optional private-network access only.
// The private device connection belongs to DeviceConnectionService.
class JunGoVpnService : VpnService() {
    private val repository get() = (application as JunGoApplication).repository
    private val observerKey = "vpn:${System.identityHashCode(this)}"
    private var starting: Job? = null

    override fun onCreate() {
        super.onCreate(); repository.attachVpn(this); repository.observe(observerKey, true)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val note = notification("network", "军哥互联", "正在维护系统 VPN 连接")
        if (Build.VERSION.SDK_INT >= 34) startForeground(100, note, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE) else startForeground(100, note)
        val preferences = getSharedPreferences("network", MODE_PRIVATE)
        // Fresh intents carry privateAccess/proxy. Pre-release builds sent the old
        // "mesh" switch, and that switch did drive the VPN, so it is read as private
        // access when a restored intent still only has the legacy field.
        val privateAccess = when {
            intent == null -> preferences.privateAccess()
            intent.hasExtra("privateAccess") -> intent.getBooleanExtra("privateAccess", false)
            intent.hasExtra("mesh") -> intent.getBooleanExtra("mesh", false)
            else -> preferences.privateAccess()
        }
        val proxy = intent?.getBooleanExtra("proxy", preferences.getBoolean("proxy", false)) ?: preferences.getBoolean("proxy", false)
        val previous = starting
        previous?.cancel()
        starting = repository.scope.launch {
            previous?.join()
            try {
                if (NetworkPolicy.desiredVPN(privateAccess, proxy)) {
                    repository.configureVpn(this@JunGoVpnService, privateAccess, proxy) {
                        val descriptor = Builder().setSession("军哥互联").setMtu(1280)
                            .addAddress("172.19.0.1", 30).addAddress("fdfe:dcba:9876::1", 126)
                            .addRoute("0.0.0.0", 0).addRoute("::", 0).addDnsServer("172.19.0.2")
                            .addDisallowedApplication(packageName)
                            .setConfigureIntent(PendingIntent.getActivity(this@JunGoVpnService, 0, Intent(this@JunGoVpnService, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE))
                            .establish() ?: error("系统未授予 VPN 连接权限")
                        descriptor.detachFd()
                    }
                    repository.refresh()
                } else {
                    clearDesiredVPN(preferences)
                    repository.syncNetworkFlags()
                    repository.stopVpn(this@JunGoVpnService)
                    stopSelfResult(startId)
                }
            } catch (e: Exception) {
                if (e is kotlinx.coroutines.CancellationException) throw e
                repository.reportError(e.message ?: "VPN 启动失败")
                repository.safely { repository.stopVpn(this@JunGoVpnService) }
                stopSelfResult(startId)
            }
        }
        return START_STICKY
    }

    // Revocation clears the desired switches only: the device connection of a
    // paired phone keeps running without the TUN.
    override fun onRevoke() {
        starting?.cancel()
        clearDesiredVPN(getSharedPreferences("network", MODE_PRIVATE)); repository.syncNetworkFlags()
        repository.scope.launch {
            try { repository.safely { repository.stopVpn(this@JunGoVpnService) } }
            finally { stopSelf() }
        }
        super.onRevoke()
    }
    override fun onDestroy() {
        starting?.cancel()
        repository.scope.launch { repository.safely { repository.stopVpn(this@JunGoVpnService, detach = true) }; repository.observe(observerKey, false) }
        stopForeground(STOP_FOREGROUND_REMOVE)
        super.onDestroy()
    }
}

/** Desired private-network VPN access, with the pre-release "mesh" switch as the migration source. */
private fun SharedPreferences.privateAccess(): Boolean = getBoolean("privateAccess", getBoolean("mesh", false))

private fun clearDesiredVPN(preferences: SharedPreferences) {
    preferences.edit().remove("privateAccess").remove("proxy").remove("mesh").apply()
}

// Foreground lifetime follows real native tasks, independent of Activity or
// folding changes. Android's dataSync deadline pauses tasks for later recovery.
class TransferService : Service() {
    private val repository get() = (application as JunGoApplication).repository
    private val observerKey = "transfers:${System.identityHashCode(this)}"
    private var watch: Job? = null
    private var wakeLock: PowerManager.WakeLock? = null
    private var notifiedText: String? = null
    override fun onBind(intent: Intent?): IBinder? = null
    override fun onCreate() {
        super.onCreate()
        wakeLock = getSystemService(PowerManager::class.java).newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "JunGo:transfer").apply { setReferenceCounted(false) }
        repository.observe(observerKey, true)
    }
    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        startForeground(101, notification("transfers", "军哥互联 · 文件传输", notifiedText ?: "传输任务运行中"), ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
        if (watch == null) watch = repository.scope.launch {
            combine(repository.state, repository.preparations) { state, preparing -> state to preparing }.collectLatest { (state, preparing) ->
                if (state.initialized && state.transfers.none { it.active } && state.activeIncoming == 0 && preparing == 0) stopSelf()
                else {
                    val computing = preparing > 0 || state.activeIncoming > 0 || state.transfers.any { it.status == "running" || it.status == "hashing" }
                    if (computing && wakeLock?.isHeld == false) wakeLock?.acquire(6 * 60 * 60 * 1000L)
                    if (!computing && wakeLock?.isHeld == true) wakeLock?.release()
                    val text = if (preparing > 0) "正在整理文件和传输队列" else "${state.transfers.count { it.active } + state.activeIncoming} 个任务进行中"
                    if (text != notifiedText) {
                        getSystemService(NotificationManager::class.java).notify(101, notification("transfers", "军哥互联 · 文件传输", text))
                        notifiedText = text
                    }
                }
            }
        }
        return START_NOT_STICKY
    }
    override fun onTimeout(startId: Int, fgsType: Int) {
        val preparations = repository.beginTransferTimeout()
        repository.scope.launch {
            try {
            preparations.forEach { it.join() }
            repository.safely { repository.refresh() }
            repository.state.value.transfers.filter { it.active }.forEach { task -> repository.safely { repository.request("transferAction", json("id" to task.id, "action" to "pause")) } }
            repository.reportError("系统已达到后台传输时限，任务已暂停，可回到应用继续。")
            } finally { repository.finishTransferTimeout() }
        }
        stopSelf()
    }
    override fun onDestroy() { watch?.cancel(); if (wakeLock?.isHeld == true) wakeLock?.release(); repository.observe(observerKey, false); stopForeground(STOP_FOREGROUND_REMOVE); super.onDestroy() }
}
