# SPDX-License-Identifier: GPL-3.0-or-later
# Manifest components and androidx.webkit's listener are kept by the default
# rules. BouncyCastle's providers are looked up by name.
-keep class org.bouncycastle.** { *; }
-dontwarn org.bouncycastle.**
-dontwarn okhttp3.internal.platform.**
-dontwarn org.conscrypt.**
-dontwarn org.openjsse.**
