package com.junge.connect

import android.Manifest
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.repeatOnLifecycle
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.launch

class MainActivity : ComponentActivity() {
    private val repository get() = (application as JunGoApplication).repository
    private val observerKey = "activity:${System.identityHashCode(this)}"
    private var pendingNetwork: Pair<Boolean, Boolean>? = null
    private val notificationPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { }
    private val vpnPermission = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
        if (result.resultCode == RESULT_OK) pendingNetwork?.let { launchVpn(it.first, it.second) }
        else repository.reportError("未启用系统 VPN，设备消息与文件连接不受影响。")
        pendingNetwork = null
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        if (savedInstanceState == null) repository.receiveShare(intent)
        if (savedInstanceState?.containsKey("pendingPrivateAccess") == true) pendingNetwork = savedInstanceState.getBoolean("pendingPrivateAccess") to savedInstanceState.getBoolean("pendingProxy")
        setContent { JunGoTheme { JunGoApp(repository, ::requestNetwork) } }
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                repository.state.map { state -> state.transfers.any { it.active } || state.activeIncoming > 0 }.distinctUntilChanged().collect { active ->
                    if (active && !repository.transferTimeoutDraining) runCatching { startForegroundService(Intent(this@MainActivity, TransferService::class.java)) }
                        .onFailure { repository.reportError(it.message ?: "后台传输服务启动失败") }
                }
            }
        }
        // A visible Activity raises the private device connection for a paired
        // phone; the repository refuses while the user has paused it, and the
        // service is never started from the Application object.
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                repository.state.map { state -> state.paired }.distinctUntilChanged().collect { paired ->
                    if (paired) repository.ensureDeviceConnections(this@MainActivity)
                }
            }
        }
    }
    override fun onNewIntent(intent: Intent) { super.onNewIntent(intent); setIntent(intent); repository.receiveShare(intent) }
    override fun onStart() { super.onStart(); repository.observe(observerKey, true) }
    override fun onStop() { repository.observe(observerKey, false); super.onStop() }
    override fun onSaveInstanceState(outState: Bundle) {
        pendingNetwork?.let { outState.putBoolean("pendingPrivateAccess", it.first); outState.putBoolean("pendingProxy", it.second) }
        super.onSaveInstanceState(outState)
    }

    /** The first switch is private-network VPN access, not the device connection. */
    private fun requestNetwork(privateAccess: Boolean, proxy: Boolean) {
        if (!NetworkPolicy.desiredVPN(privateAccess, proxy)) {
            // Route all VPN changes through the service's ordered lifecycle;
            // an old Activity stop must never tear down a newer start.
            launchVpn(false, false)
            return
        }
        // Turning private access on is an explicit user action that also
        // resumes the device connection for a paired phone.
        if (privateAccess && !repository.privateAccessDesired()) repository.ensureDeviceConnections(this, resume = true)
        val permission = VpnService.prepare(this)
        if (permission != null) { pendingNetwork = privateAccess to proxy; vpnPermission.launch(permission) }
        else launchVpn(privateAccess, proxy)
    }

    private fun launchVpn(privateAccess: Boolean, proxy: Boolean) {
        runCatching { startForegroundService(Intent(this, JunGoVpnService::class.java).putExtra("privateAccess", privateAccess).putExtra("proxy", proxy)) }
            .onFailure { repository.reportError(it.message ?: "无法启动连接服务") }
        if (Build.VERSION.SDK_INT >= 33 && NetworkPolicy.desiredVPN(privateAccess, proxy)) notificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
    }
}
