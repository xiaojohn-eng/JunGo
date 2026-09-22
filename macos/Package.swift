// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "JunGoMenu",
    platforms: [.macOS(.v13)],
    products: [.executable(name: "JunGoMenu", targets: ["JunGoMenu"])],
    targets: [
        .executableTarget(name: "JunGoMenu"),
    ]
)
