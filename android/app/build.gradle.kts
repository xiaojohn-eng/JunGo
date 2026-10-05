plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

val coreApiCheck = providers.gradleProperty("coreApiCheck").orNull == "true"

android {
    namespace = "com.junge.connect"
    compileSdk = 36
    buildToolsVersion = "36.0.0"
    defaultConfig {
        applicationId = "com.junge.connect"
        minSdk = 31
        targetSdk = 36
        versionCode = 5
        versionName = "0.2.3-preview"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        ndk { abiFilters += "arm64-v8a" }
    }
    buildTypes {
        getByName("release") {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            // Explicit local preview signing preserves installed user data.
            // Ordinary release builds remain unsigned for a real release key.
            if (providers.gradleProperty("previewSigning").orNull == "true") signingConfig = signingConfigs.getByName("debug")
        }
    }
    buildFeatures { compose = true; buildConfig = true }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    packaging {
        jniLibs.useLegacyPackaging = false
        resources.excludes += "/META-INF/{AL2.0,LGPL2.1}"
    }
}

dependencies {
    if (coreApiCheck) compileOnly(files("build/core-api-check/core-api.jar"))
    else implementation(files(providers.gradleProperty("nativeAar").orNull ?: "libs/jungo.aar"))
    implementation(platform("androidx.compose:compose-bom:2025.08.01"))
    implementation("androidx.activity:activity-compose:1.10.1")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-extended")
    implementation("androidx.compose.ui:ui-tooling-preview")
    debugImplementation("androidx.compose.ui:ui-tooling")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.9.2")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.9.2")
    implementation("androidx.window:window:1.4.0")
    implementation("androidx.documentfile:documentfile:1.1.0")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.10.2")
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20250517")
    androidTestImplementation(platform("androidx.compose:compose-bom:2025.08.01"))
    androidTestImplementation("androidx.compose.ui:ui-test-junit4")
    androidTestImplementation("androidx.test.ext:junit:1.2.1")
    debugImplementation("androidx.compose.ui:ui-test-manifest")
}

// API signature checks can run before gomobile finishes. Never package an APK
// with missing native code; actual builds require the real generated AAR.
tasks.configureEach {
    if (name.startsWith("package") && !name.contains("Resources")) doFirst {
        check(!coreApiCheck) { "API-check mode cannot package an APK. Build the real jungo.aar first." }
    }
}

// Bundle the same root notices into every APK; only these explicit public
// inputs are copied, never workspace state or device credentials.
val bundleThirdPartyNotices = tasks.register<Sync>("bundleThirdPartyNotices") {
    from(rootDir.resolve("../licenses")) { into("licenses") }
    from(rootDir.resolve("../THIRD_PARTY_NOTICES.md"))
    into(layout.buildDirectory.dir("generated/notices"))
}
android.sourceSets.getByName("main").assets.srcDir(layout.buildDirectory.dir("generated/notices"))
tasks.named("preBuild").configure { dependsOn(bundleThirdPartyNotices) }

// A release APK records the exact Git commit used for its Android resources.
// Source archives without .git remain buildable and identify that provenance.
val bundleReleaseCommit = tasks.register("bundleReleaseCommit") {
    val outputDir = layout.buildDirectory.dir("generated/release-metadata")
    outputs.dir(outputDir)
    outputs.upToDateWhen { false }
    doLast {
        val revision = try {
            val process = ProcessBuilder("git", "rev-parse", "HEAD")
                .directory(rootDir.parentFile).start()
            val value = process.inputStream.bufferedReader().readText().trim()
            if (process.waitFor() == 0 && value.matches(Regex("[0-9a-f]{40}"))) value else "source-archive"
        } catch (_: Exception) { "source-archive" }
        outputDir.get().file("jungo-release-commit.txt").asFile.apply {
            parentFile.mkdirs()
            writeText("$revision\n")
        }
    }
}
android.sourceSets.getByName("main").assets.srcDir(layout.buildDirectory.dir("generated/release-metadata"))
tasks.named("preBuild").configure { dependsOn(bundleReleaseCommit) }
