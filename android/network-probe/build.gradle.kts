plugins { id("com.android.application") }
android {
    namespace = "com.junge.connect.probe"
    compileSdk = 36
    buildToolsVersion = "36.0.0"
    defaultConfig { applicationId = "com.junge.connect.probe"; minSdk = 31; targetSdk = 36; versionCode = 1; versionName = "qa-only" }
    compileOptions { sourceCompatibility = JavaVersion.VERSION_17; targetCompatibility = JavaVersion.VERSION_17 }
}
