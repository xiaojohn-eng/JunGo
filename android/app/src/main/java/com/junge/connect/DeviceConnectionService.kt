package com.junge.connect

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import android.os.PowerManager
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.distinctUntilChanged

/**
 * Owns the private device connection (native mesh) of a paired phone.
 *
 * It is independent of the system VPN: messaging and file transfer keep working
 * while the TUN is stopped, and the user can pause the device connection without
 * touching proxy policy. A sticky restart of an unpaired or paused device stops
 * immediately, so nothing silently reconnects behind the user's back.
 */
class DeviceConnectionService : Service() {
    private val repository get() = (application as JunGoApplication).repository
    private val observerKey = "devices:${System.identityHashCode(this)}"
    private var connection: Job? = null
    private var pendingPause: Job? = null
    private var incomingWatch: Job? = null
    private var incomingWake: PowerManager.WakeLock? = null
    private var incomingWakeRenewAt = 0L
    private var notifiedStatus: String? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        repository.observe(observerKey, true)
        // Incoming device traffic needs the process awake even with no VPN.
        incomingWake = getSystemService(PowerManager::class.java).newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "JunGo:inbox").apply { setReferenceCounted(false) }
        incomingWatch = repository.scope.launch { repository.state.map { it.activeIncoming > 0 }.distinctUntilChanged().collect { incoming ->
            maintainIncomingWake(incoming)
        } }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val note = deviceConnectionNotification(statusLabel())
        if (Build.VERSION.SDK_INT >= 34) startForeground(NOTIFICATION_ID, note, ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE)
        else startForeground(NOTIFICATION_ID, note)
        if (intent?.action == ACTION_PAUSE) {
            // Persist the user's choice first: a crash, restart or later
            // auto-start must never undo an explicit pause.
            getSharedPreferences("network", MODE_PRIVATE).edit().putBoolean("connectionsPaused", true).apply()
            repository.syncNetworkFlags()
            val oldConnection = connection
            val oldPause = pendingPause
            oldConnection?.cancel(); connection = null
            pendingPause = repository.scope.launch {
                oldPause?.join()
                oldConnection?.join()
                repository.safely { repository.request("network", json("mesh" to false)); repository.refresh() }
                stopSelfResult(startId)
            }
            return START_NOT_STICKY
        }
        // The pause flag is read from its own storage here so an explicit pause
        // can never be undone by a stale in-memory snapshot.
        // On a sticky process restart the initial snapshot is still unpaired;
        // keepConnected refreshes persisted identity before checking that flag.
        if (repository.connectionsPaused()) {
            stopSelfResult(startId)
            return START_NOT_STICKY
        }
        if (connection == null) {
            val pause = pendingPause
            connection = repository.scope.launch { pause?.join(); keepConnected() }
        }
        return START_STICKY
    }

    /**
     * The VPN is only ever raised for private access or the public proxy, so an
     * idle device connection refreshes native state and re-raises the mesh when
     * it is not running yet. Failures are retried on the next tick.
     */
    private suspend fun keepConnected() {
        while (true) {
            try {
                if (!repository.state.value.initialized) repository.refresh()
                val state = repository.state.value
                if (!NetworkPolicy.deviceConnectionActive(state.paired, state.connectionsPaused)) { stopSelf(); return }
                if (!state.meshRunning) { repository.request("network", json("mesh" to true)); repository.refresh() }
                maintainIncomingWake(repository.state.value.activeIncoming > 0)
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                // Never echo native parameters or pairing material.
                repository.reportError("设备连接暂不可达，将自动重试。")
            }
            val status = statusLabel()
            if (notifiedStatus != status) {
                getSystemService(NotificationManager::class.java).notify(NOTIFICATION_ID, deviceConnectionNotification(status))
                notifiedStatus = status
            }
            delay(REFRESH_INTERVAL)
        }
    }

    private fun maintainIncomingWake(active: Boolean) {
        val now = android.os.SystemClock.elapsedRealtime()
        if (active && (incomingWake?.isHeld == false || now >= incomingWakeRenewAt)) {
            incomingWake?.acquire(6 * 60 * 60 * 1000L)
            // Renew before expiry, including when no boolean transition occurs.
            incomingWakeRenewAt = now + 5 * 60 * 60 * 1000L
        } else if (!active) {
            if (incomingWake?.isHeld == true) incomingWake?.release()
            incomingWakeRenewAt = 0L
        }
    }

    private fun statusLabel(): String = when {
        repository.connectionsPaused() -> "已暂停"
        repository.state.value.meshRunning -> "在线"
        else -> "连接中"
    }

    override fun onDestroy() {
        connection?.cancel(); incomingWatch?.cancel()
        if (incomingWake?.isHeld == true) incomingWake?.release()
        repository.observe(observerKey, false)
        stopForeground(STOP_FOREGROUND_REMOVE)
        super.onDestroy()
    }

    companion object {
        const val ACTION_PAUSE = "com.junge.connect.action.PAUSE_DEVICE_CONNECTION"
        private const val NOTIFICATION_ID = 102
        private const val REFRESH_INTERVAL = 10_000L
    }
}

internal fun Service.deviceConnectionNotification(status: String): Notification {
    val manager = getSystemService(NotificationManager::class.java)
    manager.createNotificationChannel(NotificationChannel("devices", "设备消息与文件", NotificationManager.IMPORTANCE_LOW))
    val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    val pause = PendingIntent.getService(this, 1, Intent(this, DeviceConnectionService::class.java).setAction(DeviceConnectionService.ACTION_PAUSE), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    return Notification.Builder(this, "devices").setSmallIcon(R.drawable.ic_jungo).setContentTitle("设备消息与文件")
        .setContentText(status).setContentIntent(open)
        .addAction(Notification.Action.Builder(null, "暂停设备连接", pause).build())
        .setOngoing(true).setOnlyAlertOnce(true).build()
}
