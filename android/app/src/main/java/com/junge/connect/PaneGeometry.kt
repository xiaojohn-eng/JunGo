package com.junge.connect

data class PaneRect(val x: Int, val y: Int, val width: Int, val height: Int)
data class PaneGeometry(val first: PaneRect, val second: PaneRect)

// All inputs use pixels in the content area's coordinate space. Negative or
// offscreen hinge coordinates are clamped so freeform windows remain measurable.
fun calculatePanes(width: Int, height: Int, hingeStart: Int?, hingeEnd: Int?, horizontal: Boolean, gap: Int, preferredListWidth: Int): PaneGeometry {
    require(width >= 0 && height >= 0 && gap >= 0)
    if (horizontal && hingeStart != null && hingeEnd != null) {
        val start = hingeStart.coerceIn(0, height)
        val end = hingeEnd.coerceIn(start, height)
        val firstHeight = (start - gap / 2).coerceAtLeast(0)
        val secondY = (end + gap / 2).coerceAtMost(height)
        return PaneGeometry(PaneRect(0, 0, width, firstHeight), PaneRect(0, secondY, width, height - secondY))
    }
    val firstWidth: Int
    val secondX: Int
    if (hingeStart != null && hingeEnd != null) {
        firstWidth = (hingeStart - gap / 2).coerceIn(0, width)
        secondX = (hingeEnd + gap / 2).coerceIn(firstWidth, width)
    } else {
        firstWidth = minOf(preferredListWidth, (width * .36f).toInt()).coerceIn(0, width)
        secondX = (firstWidth + gap).coerceAtMost(width)
    }
    return PaneGeometry(PaneRect(0, 0, firstWidth, height), PaneRect(secondX, 0, width - secondX, height))
}
