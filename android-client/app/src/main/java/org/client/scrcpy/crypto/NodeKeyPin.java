package org.client.scrcpy.crypto;

import java.security.GeneralSecurityException;
import java.security.MessageDigest;
import java.security.cert.CertificateException;
import java.security.cert.X509Certificate;
import java.util.Base64;

import javax.net.ssl.SSLContext;
import javax.net.ssl.SSLSocketFactory;
import javax.net.ssl.TrustManager;
import javax.net.ssl.X509TrustManager;

/**
 * Trusts exactly one runtime node: the one whose TLS public key hashes to the pin
 * the control plane issued with the session. Nodes use self-signed certificates
 * reached by IP address, so the pin replaces chain and hostname checks.
 */
public final class NodeKeyPin {
    private NodeKeyPin() {
    }

    /** Base64 SHA-256 of the certificate's SubjectPublicKeyInfo, as the node reports it. */
    public static String spkiSha256(X509Certificate certificate) throws GeneralSecurityException {
        byte[] digest = MessageDigest.getInstance("SHA-256").digest(certificate.getPublicKey().getEncoded());
        return Base64.getEncoder().encodeToString(digest);
    }

    public static X509TrustManager trustManager(String pin) {
        return new X509TrustManager() {
            @Override
            public void checkClientTrusted(X509Certificate[] chain, String authType) throws CertificateException {
                throw new CertificateException("client certificates are not accepted");
            }

            @Override
            public void checkServerTrusted(X509Certificate[] chain, String authType) throws CertificateException {
                if (chain == null || chain.length == 0) {
                    throw new CertificateException("runtime node presented no certificate");
                }
                String actual;
                try {
                    actual = spkiSha256(chain[0]);
                } catch (GeneralSecurityException e) {
                    throw new CertificateException("hash runtime node key", e);
                }
                if (!MessageDigest.isEqual(actual.getBytes(), pin.getBytes())) {
                    throw new CertificateException("runtime node key does not match its pin");
                }
            }

            @Override
            public X509Certificate[] getAcceptedIssuers() {
                return new X509Certificate[0];
            }
        };
    }

    public static SSLSocketFactory socketFactory(String pin) throws GeneralSecurityException {
        SSLContext context = SSLContext.getInstance("TLS");
        context.init(null, new TrustManager[]{trustManager(pin)}, null);
        return context.getSocketFactory();
    }
}
