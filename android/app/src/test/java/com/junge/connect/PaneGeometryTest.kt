package com.junge.connect

import org.junit.Assert.*
import org.junit.Test

class PaneGeometryTest {
    @Test fun verticalHingeNeverReceivesPaneContent() {
        val panes = calculatePanes(900, 700, 420, 448, false, 16, 300)
        assertTrue(panes.first.x + panes.first.width <= 420)
        assertTrue(panes.second.x >= 448)
        assertEquals(900, panes.second.x + panes.second.width)
        assertEquals(700, panes.first.height)
    }

    @Test fun horizontalHingeUsesTopAndBottomPanes() {
        val panes = calculatePanes(560, 780, 360, 390, true, 16, 300)
        assertTrue(panes.first.y + panes.first.height <= 360)
        assertTrue(panes.second.y >= 390)
        assertEquals(780, panes.second.y + panes.second.height)
        assertEquals(560, panes.second.width)
    }

    @Test fun resizingAndOffscreenHingesNeverProduceNegativeConstraints() {
        for (width in listOf(0, 10, 320, 600, 840, 1800)) {
            for (height in listOf(0, 10, 600)) {
                for (horizontal in listOf(false, true)) {
                    for (hinge in listOf(-30, 0, 350, 2000)) {
                        val panes = calculatePanes(width, height, hinge, hinge + 10, horizontal, 16, 300)
                        for (pane in listOf(panes.first, panes.second)) {
                            assertTrue(pane.width >= 0 && pane.height >= 0)
                            assertTrue(pane.x + pane.width <= width)
                            assertTrue(pane.y + pane.height <= height)
                        }
                    }
                }
            }
        }
    }
}
