package io.virtroid.client.viewer

import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.ServiceConnection
import android.os.Build
import android.os.IBinder
import android.view.MotionEvent
import android.view.Surface
import org.client.scrcpy.Scrcpy
import kotlin.concurrent.thread

class ScrcpySessionHost(
    private val context: Context,
    private val relayHost: String,
    private val relayPort: Int,
    private val relayTls: Boolean,
    private val relayPath: String,
    private val relayToken: String,
    private val viewerPublicKey: String,
    private val audioEnabled: Boolean,
    private val surface: Surface,
    private val displayWidth: Int,
    private val displayHeight: Int,
    private val callback: Callback,
) : Scrcpy.ServiceCallbacks {
    private var scrcpy: Scrcpy? = null
    private var serviceBound = false
    private var destroyed = false
    private var displayLandscape = displayWidth > displayHeight

    private val serviceConnection = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName?, binder: IBinder?) {
            val service = (binder as? Scrcpy.MyServiceBinder)?.service ?: run {
                callback.onDisconnected("scrcpy binder unavailable")
                return
            }
            scrcpy = service
            service.setServiceCallbacks(this@ScrcpySessionHost)
            serviceBound = true
            service.start(
                surface,
                relayHost,
                relayPort,
                relayTls,
                relayPath,
                relayToken,
                viewerPublicKey,
                audioEnabled,
                displayHeight,
                displayWidth,
                50,
            )

            thread(name = "scrcpy-connect-watch") {
                var attempts = 100
                while (!destroyed && attempts > 0 && service.check_socket_connection().not()) {
                    Thread.sleep(100)
                    attempts--
                }

                if (destroyed) {
                    return@thread
                }

                if (!service.check_socket_connection()) {
                    callback.onDisconnected("runtime stream timed out")
                    return@thread
                }

                val resolution = service.get_remote_device_resolution()
                displayLandscape = displayWidth > displayHeight
                callback.onConnected(resolution[0], resolution[1])
            }
        }

        override fun onServiceDisconnected(name: ComponentName?) {
            serviceBound = false
            scrcpy = null
        }
    }

    fun connect() {
        val intent = Intent(context, Scrcpy::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            context.startForegroundService(intent)
        } else {
            context.startService(intent)
        }
        context.bindService(intent, serviceConnection, Context.BIND_AUTO_CREATE)
    }

    fun pauseRendering() {
        scrcpy?.pause()
    }

    fun attachSurface(surface: Surface, width: Int, height: Int) {
        displayLandscape = width > height
        scrcpy?.setParms(surface, width, height)
        scrcpy?.resume()
    }

    fun sendTouch(event: MotionEvent, width: Int, height: Int): Boolean {
        displayLandscape = width > height
        return scrcpy?.touchevent(event, displayLandscape, width, height) ?: false
    }

    fun sendKey(keyCode: Int) {
        scrcpy?.sendKeyevent(keyCode)
    }

    fun destroy() {
        destroyed = true
        scrcpy?.StopService()
        if (serviceBound) {
            runCatching { context.unbindService(serviceConnection) }
            serviceBound = false
        }
        context.stopService(Intent(context, Scrcpy::class.java))
    }

    override fun loadNewRotation() {
        val resolution = scrcpy?.get_remote_device_resolution() ?: return
        displayLandscape = displayWidth > displayHeight
        callback.onConnected(resolution[0], resolution[1])
    }

    override fun errorDisconnect() {
        callback.onDisconnected("runtime stream disconnected")
    }

    override fun firstVideoFrame() {
        callback.onFirstVideoFrame()
    }

    interface Callback {
        fun onConnected(remoteWidth: Int, remoteHeight: Int)
        fun onFirstVideoFrame()
        fun onDisconnected(message: String)
    }
}
