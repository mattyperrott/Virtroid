// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.ui

import android.os.Bundle
import androidx.appcompat.app.AppCompatActivity
import io.virtroid.shield.R
import io.virtroid.shield.databinding.ActivityAboutBinding
import io.virtroid.shield.engine.EngineLog

/** The licence, where the source is, what the engine bundles, and the engine's recent warnings. */
class AboutActivity : AppCompatActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val binding = ActivityAboutBinding.inflate(layoutInflater)
        setContentView(binding.root)
        binding.closeButton.setOnClickListener { finish() }
        val packages = runCatching { assets.open("engine/bundled-packages.txt").bufferedReader().readText() }.getOrDefault("")
        binding.packages.text = getString(R.string.about_packages, packages.trim())
        val log = EngineLog.snapshot()
        binding.log.text = if (log.isEmpty()) getString(R.string.about_log_empty) else log.joinToString("\n")
    }
}
