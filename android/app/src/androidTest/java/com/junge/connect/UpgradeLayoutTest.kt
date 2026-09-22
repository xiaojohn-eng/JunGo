package com.junge.connect

import androidx.compose.foundation.layout.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.test.platform.app.InstrumentationRegistry
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test

/** Isolated visual fixtures: no subscription, pairing or transport commands. */
class UpgradeLayoutTest {
    @get:Rule val compose = createComposeRule()
    private val repository get() = (InstrumentationRegistry.getInstrumentation().targetContext.applicationContext as JunGoApplication).repository
    private val peer = Peer("ui-preview-mac", "本机 Mac", "", "", "relay")
    private fun message(text: String) = ChatMessage.parse(JSONObject("""{"id":"ui-preview-msg","deviceId":"ui-preview-mac","direction":"outgoing","kind":"text","text":"$text","status":"sent","created":"2026-09-22T06:32:00Z"}"""))

    @Test fun shortMessageHugsContentAndComposerRemainsVisibleWithKeyboard() {
        compose.setContent { JunGoTheme { Box(Modifier.fillMaxSize()) { ChatPane(peer, listOf(message("在吗")), false, repository, {}, {}) } } }
        compose.onNodeWithText("在吗").assertIsDisplayed()
        val bounds=compose.onNodeWithText("在吗").getUnclippedBoundsInRoot()
        assertTrue("Short message text width",bounds.right - bounds.left < 100.dp)
        compose.onNodeWithText("发消息或文件").performClick()
        compose.waitForIdle()
        compose.onNodeWithContentDescription("发送文字").assertIsDisplayed()
        compose.onNodeWithText("在吗").assertIsDisplayed()
        compose.onNodeWithContentDescription("添加附件").performClick()
        compose.onNodeWithText("照片").assertIsDisplayed()
        compose.onNodeWithText("文件夹 ZIP").assertIsDisplayed()
    }

    @Test fun activeFileNeverShowsCompletedStatus() {
        val file=message("").copy(kind="file",name="项目资料.zip",size=1000,completed=650,status="running")
        compose.setContent { JunGoTheme { Column { MessageBubble(file,repository,{}) } } }
        compose.onNodeWithText("发送中").assertIsDisplayed()
        compose.onNodeWithText("已发送").assertDoesNotExist()
        compose.onNodeWithText("已接收").assertDoesNotExist()
    }
}
