import AppKit
import Foundation

guard CommandLine.arguments.count == 2 else {
    fputs("Usage: swift GenerateIcon.swift <output.iconset>\n", stderr)
    exit(1)
}
let directory = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)

// A small original vector mark, rendered at each required icon resolution.
func render(size: Int) throws -> Data {
    let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: size, pixelsHigh: size,
                                  bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
                                  isPlanar: false, colorSpaceName: .deviceRGB,
                                  bytesPerRow: 0, bitsPerPixel: 0)!
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)
    let context = NSGraphicsContext.current!.cgContext
    context.scaleBy(x: CGFloat(size) / 1024, y: CGFloat(size) / 1024)
    let tile = NSBezierPath(roundedRect: NSRect(x: 66, y: 66, width: 892, height: 892), xRadius: 202, yRadius: 202)
    let gradient = NSGradient(starting: NSColor(srgbRed: 0.04, green: 0.36, blue: 0.98, alpha: 1),
                              ending: NSColor(srgbRed: 0.18, green: 0.63, blue: 1, alpha: 1))!
    gradient.draw(in: tile, angle: 90)
    context.translateBy(x: 512, y: 512)
    context.rotate(by: -.pi / 4)
    NSColor.white.setStroke()
    for y: CGFloat in [-225, 15] {
        let link = NSBezierPath(roundedRect: NSRect(x: -104, y: y, width: 208, height: 285), xRadius: 102, yRadius: 102)
        link.lineWidth = 48
        link.stroke()
    }
    let bridge = NSBezierPath()
    bridge.move(to: NSPoint(x: 0, y: -48))
    bridge.line(to: NSPoint(x: 0, y: 120))
    bridge.lineWidth = 48
    bridge.lineCapStyle = .round
    bridge.stroke()
    NSGraphicsContext.restoreGraphicsState()
    guard let png = bitmap.representation(using: .png, properties: [:]) else {
        throw NSError(domain: "JunGoIcon", code: 1)
    }
    return png
}

for base in [16, 32, 128, 256, 512] {
    try render(size: base).write(to: directory.appendingPathComponent("icon_\(base)x\(base).png"))
    try render(size: base * 2).write(to: directory.appendingPathComponent("icon_\(base)x\(base)@2x.png"))
}
