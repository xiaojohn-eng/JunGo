package com.junge.connect

/**
 * Pure decisions shared by the UI, the VPN service and the device connection
 * service. Android types are intentionally absent so the JVM tests can
 * enumerate every combination.
 */
object NetworkPolicy {
    /**
     * The system TUN is only needed when another app must reach the private
     * network, or when the public proxy is on. Device messaging and files never
     * require it.
     */
    fun desiredVPN(privateAccess: Boolean, proxy: Boolean): Boolean = privateAccess || proxy

    /**
     * The private device connection belongs to a paired phone until the user
     * pauses it; VPN switches must never bring it back on their own.
     */
    fun deviceConnectionActive(paired: Boolean, paused: Boolean): Boolean = paired && !paused
}
