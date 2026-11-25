plugins {
    id("com.android.application")
    // The Flutter Gradle Plugin must be applied after the Android and Kotlin Gradle plugins.
    id("dev.flutter.flutter-gradle-plugin")
}

android {
    namespace = "org.openfimra.field"
    compileSdk = flutter.compileSdkVersion
    ndkVersion = flutter.ndkVersion

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
        // flutter_local_notifications needs newer Java APIs on older Android versions.
        isCoreLibraryDesugaringEnabled = true
    }

    defaultConfig {
        // The sign-in return address uses this ID as its scheme: org.openfimra.field:/callback
        applicationId = "org.openfimra.field"
        // MapLibre Android needs API level 24 or later.
        minSdk = maxOf(flutter.minSdkVersion, 24)
        targetSdk = flutter.targetSdkVersion
        versionCode = flutter.versionCode
        versionName = flutter.versionName
    }

    buildTypes {
        release {
            // Test builds are signed with the debug key. A department signs its
            // own release build with its own key, which is never committed.
            signingConfig = signingConfigs.getByName("debug")
        }
    }
}

kotlin {
    compilerOptions {
        jvmTarget = org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17
    }
}

flutter {
    source = "../.."
}

// The UnifiedPush connector asks for Tink's Java build and flutter_secure_storage
// for its Android build. They hold the same classes, so use the Android build only.
configurations.all {
    resolutionStrategy.dependencySubstitution {
        substitute(module("com.google.crypto.tink:tink")).using(module("com.google.crypto.tink:tink-android:1.24.0"))
    }
}

dependencies {
    coreLibraryDesugaring("com.android.tools:desugar_jdk_libs:2.1.4")
}
