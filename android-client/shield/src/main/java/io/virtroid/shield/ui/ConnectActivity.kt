// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.ui

import android.content.ActivityNotFoundException
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.Color
import android.net.Uri
import android.os.Bundle
import android.view.View
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import com.google.zxing.BarcodeFormat
import com.google.zxing.EncodeHintType
import com.google.zxing.qrcode.QRCodeWriter
import io.virtroid.shield.R
import io.virtroid.shield.ShieldApp
import io.virtroid.shield.databinding.ActivityConnectBinding
import io.virtroid.shield.engine.EngineException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Connects the user's own wallet over WalletConnect: a wallet on this cloud
 * phone opens the wc: link, one on the user's real phone scans the code.
 * Either way the wallet asks the user to approve, and keeps the keys.
 */
class ConnectActivity : AppCompatActivity() {
    private lateinit var binding: ActivityConnectBinding
    private val app get() = application as ShieldApp

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityConnectBinding.inflate(layoutInflater)
        setContentView(binding.root)
        binding.closeButton.setOnClickListener { finish() }
        pair()
    }

    private fun pair() {
        lifecycleScope.launch {
            try {
                val chains = app.shield.chains().map { it.chainId }
                val pairing = app.walletConnect.pair(chains)
                binding.qr.setImageBitmap(withContext(Dispatchers.Default) { qr(pairing.uri) })
                binding.openWallet.setOnClickListener {
                    try {
                        startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(pairing.uri)))
                    } catch (_: ActivityNotFoundException) {
                        binding.status.setText(R.string.connect_no_wallet_here)
                    }
                }
                binding.status.setText(R.string.connect_waiting)
                val session = pairing.approved.await()
                binding.status.text = getString(R.string.connect_done, session.walletName.ifEmpty { getString(R.string.wallet_unnamed) })
                app.shield.load()
                finish()
            } catch (error: EngineException) {
                binding.status.text = Errors.describe(this@ConnectActivity, error)
                binding.retry.visibility = View.VISIBLE
            } catch (error: Exception) {
                binding.status.text = getString(R.string.connect_failed, error.message ?: error.javaClass.simpleName)
                binding.retry.visibility = View.VISIBLE
            }
        }
        binding.retry.setOnClickListener {
            binding.retry.visibility = View.GONE
            pair()
        }
    }

    private fun qr(text: String): Bitmap {
        val size = 640
        val matrix = QRCodeWriter().encode(text, BarcodeFormat.QR_CODE, size, size, mapOf(EncodeHintType.MARGIN to 1))
        val pixels = IntArray(size * size) { if (matrix.get(it % size, it / size)) Color.BLACK else Color.WHITE }
        return Bitmap.createBitmap(pixels, size, size, Bitmap.Config.RGB_565)
    }
}
