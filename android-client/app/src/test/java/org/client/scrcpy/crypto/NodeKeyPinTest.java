package org.client.scrcpy.crypto;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertThrows;

import java.io.ByteArrayInputStream;
import java.nio.charset.StandardCharsets;
import java.security.cert.CertificateException;
import java.security.cert.CertificateFactory;
import java.security.cert.X509Certificate;

import javax.net.ssl.X509TrustManager;

import org.junit.Test;

public class NodeKeyPinTest {
    // Pin computed independently with:
    // openssl x509 -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | base64
    private static final String NODE_PIN = "hN3Crvag059Pep/hXmAsIylRaq/9CIxthAjS1stC8Fw=";
    private static final String NODE_CERT =
            "-----BEGIN CERTIFICATE-----\n" +
            "MIIBbjCCARSgAwIBAgIUapANBd02UMKpqmsu2SuLQ7eiYnUwCgYIKoZIzj0EAwIw\n" +
            "HTEbMBkGA1UEAwwSdmlydHJvaWQtbm9kZSB0ZXN0MB4XDTI2MTAxMDEyMTAzMloX\n" +
            "DTM2MTAwNzEyMTAzMlowHTEbMBkGA1UEAwwSdmlydHJvaWQtbm9kZSB0ZXN0MFkw\n" +
            "EwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEA+t2SAwHc2w3Av8+C6DEVjbdkOHZ2XGS\n" +
            "7HsmR19Qa8d6RaCv9r10gkJqorC8IKQL8qJoV5yN51jOe4C7WYHEoaMyMDAwHQYD\n" +
            "VR0OBBYEFPKUM9RGYE4MqB5TC7odB/9dVG+5MA8GA1UdEwEB/wQFMAMBAf8wCgYI\n" +
            "KoZIzj0EAwIDSAAwRQIgNzFvey5aL8DjJV9+33ZdoOpAzEMutaEUC2Ms/tyH/ogC\n" +
            "IQCpvCo4/aLeBhEKMi8ZDtRtkMwALrW918GoM10gBxcQvg==\n" +
            "-----END CERTIFICATE-----\n";
    private static final String OTHER_CERT =
            "-----BEGIN CERTIFICATE-----\n" +
            "MIIBUzCB+qADAgECAhQEy5by+WDbgi9jornV7VEFpDtswTAKBggqhkjOPQQDAjAQ\n" +
            "MQ4wDAYDVQQDDAVvdGhlcjAeFw0yNjEwMTAxMjEwMzJaFw0zNjEwMDcxMjEwMzJa\n" +
            "MBAxDjAMBgNVBAMMBW90aGVyMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEk0TL\n" +
            "mw0ScOPYFIXR9G1twwjw95FLqpZ9Ddxfrb7cdSf3M3foQ2F2r3pAo1tQ1Rt/a9gz\n" +
            "1+Lx2vx/gWG9XY2apaMyMDAwHQYDVR0OBBYEFBeQ+NTVj7aXBcAmc44k5LgAZzNa\n" +
            "MA8GA1UdEwEB/wQFMAMBAf8wCgYIKoZIzj0EAwIDSAAwRQIgTulu3F88KLxu9OHv\n" +
            "TAMS4C91l0fAIqfpuD7rbeXboXsCIQCTrVzJGD393VjDr3c2dEFE1ZxXert4d9Wp\n" +
            "cSZ/MrGMzg==\n" +
            "-----END CERTIFICATE-----\n";

    @Test
    public void pinMatchesTheOpenSslDigestOfTheNodeKey() throws Exception {
        assertEquals(NODE_PIN, NodeKeyPin.spkiSha256(certificate(NODE_CERT)));
    }

    @Test
    public void trustManagerAcceptsOnlyThePinnedNode() throws Exception {
        X509TrustManager trust = NodeKeyPin.trustManager(NODE_PIN);
        trust.checkServerTrusted(new X509Certificate[]{certificate(NODE_CERT)}, "ECDHE_ECDSA");
        assertThrows(CertificateException.class, () ->
                trust.checkServerTrusted(new X509Certificate[]{certificate(OTHER_CERT)}, "ECDHE_ECDSA"));
        assertThrows(CertificateException.class, () ->
                trust.checkServerTrusted(new X509Certificate[0], "ECDHE_ECDSA"));
    }

    private static X509Certificate certificate(String pem) throws Exception {
        return (X509Certificate) CertificateFactory.getInstance("X.509")
                .generateCertificate(new ByteArrayInputStream(pem.getBytes(StandardCharsets.US_ASCII)));
    }
}
