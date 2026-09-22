package com.junge.connect

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow

/** Process-owned composer state. UI recreation never starts a second send. */
internal class ChatComposer(initial: Map<String, String>, private val persist: (String, String) -> Unit) {
    private val mutableDrafts = MutableStateFlow(initial)
    val drafts = mutableDrafts.asStateFlow()
    private val mutableSending = MutableStateFlow<Set<String>>(emptySet())
    val sending = mutableSending.asStateFlow()

    // Called on the repository's Main dispatcher, including send completions.
    fun save(device: String, text: String) {
        mutableDrafts.value = mutableDrafts.value + (device to text)
        persist(device, text)
    }

    suspend fun send(device: String, enqueue: suspend (String) -> Unit): Boolean {
        val text = mutableDrafts.value[device].orEmpty()
        if (text.isBlank() || device in mutableSending.value) return false
        mutableSending.value = mutableSending.value + device
        try {
            enqueue(text)
            // Acknowledgement belongs to this exact draft. Never erase text
            // typed while native persistence was in progress.
            if (mutableDrafts.value[device] == text) save(device, "")
            return true
        } finally {
            mutableSending.value = mutableSending.value - device
        }
    }
}
