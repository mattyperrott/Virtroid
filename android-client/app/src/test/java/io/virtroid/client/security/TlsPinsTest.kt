package io.virtroid.client.security

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import javax.net.ssl.SSLPeerUnverifiedException

class TlsPinsTest {
    @Test
    fun productionHostIsPinned() {
        assertTrue(TlsPins.hasPinsForHost("virtroid.network"))
        assertTrue(TlsPins.hasPinsForHost("WWW.VIRTROID.NETWORK"))
    }

    @Test
    fun unpinnedHostsUsePlatformTlsOnly() {
        assertFalse(TlsPins.hasPinsForHost("127.0.0.1"))
        TlsPins.checkPeerCertificates("127.0.0.1", emptyArray())
    }

    @Test(expected = SSLPeerUnverifiedException::class)
    fun pinnedHostsFailClosedWithoutPeerCertificates() {
        TlsPins.checkPeerCertificates("virtroid.network", emptyArray())
    }
}
