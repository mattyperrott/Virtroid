package io.virtroid.client.security;

import static org.junit.Assert.fail;

import java.math.BigInteger;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.MessageDigest;
import java.security.Principal;
import java.security.PublicKey;
import java.security.cert.Certificate;
import java.security.cert.X509Certificate;
import java.util.Base64;
import java.util.Collections;
import java.util.Date;
import java.util.Set;

import javax.net.ssl.SSLPeerUnverifiedException;

import org.junit.Test;

public class TlsPinsJavaTest {
    @Test
    public void pinnedRelayCheckDoesNotAcceptStuffedNonLeafCertificate() throws Exception {
        X509Certificate attackerLeaf = testCertificate(generatePublicKey());
        X509Certificate stuffedPinnedCertificate = testCertificate(generatePublicKey());
        Set<String> pins = Collections.singleton(spkiPin(stuffedPinnedCertificate));

        try {
            TlsPins.checkPeerCertificates(
                    "virtroid.network",
                    new Certificate[]{attackerLeaf, stuffedPinnedCertificate},
                    pins
            );
            fail("stuffed non-leaf certificate satisfied relay pinning");
        } catch (SSLPeerUnverifiedException expected) {
            // Expected: only the authenticated leaf certificate is eligible to satisfy the pin.
        }
    }

    @Test
    public void pinnedRelayCheckAcceptsPinnedLeafCertificate() throws Exception {
        X509Certificate pinnedLeaf = testCertificate(generatePublicKey());
        Set<String> pins = Collections.singleton(spkiPin(pinnedLeaf));

        TlsPins.checkPeerCertificates("virtroid.network", new Certificate[]{pinnedLeaf}, pins);
    }

    private static PublicKey generatePublicKey() throws Exception {
        KeyPairGenerator generator = KeyPairGenerator.getInstance("EC");
        generator.initialize(256);
        KeyPair pair = generator.generateKeyPair();
        return pair.getPublic();
    }

    private static String spkiPin(X509Certificate certificate) throws Exception {
        byte[] digest = MessageDigest.getInstance("SHA-256").digest(certificate.getPublicKey().getEncoded());
        return "sha256/" + Base64.getEncoder().encodeToString(digest);
    }

    private static X509Certificate testCertificate(PublicKey publicKey) {
        return new X509Certificate() {
            @Override
            public PublicKey getPublicKey() {
                return publicKey;
            }

            @Override
            public void checkValidity() {
            }

            @Override
            public void checkValidity(Date date) {
            }

            @Override
            public int getVersion() {
                return 3;
            }

            @Override
            public BigInteger getSerialNumber() {
                return BigInteger.ONE;
            }

            @Override
            public Principal getIssuerDN() {
                return () -> "CN=test";
            }

            @Override
            public Principal getSubjectDN() {
                return () -> "CN=test";
            }

            @Override
            public Date getNotBefore() {
                return new Date(0);
            }

            @Override
            public Date getNotAfter() {
                return new Date(Long.MAX_VALUE);
            }

            @Override
            public byte[] getTBSCertificate() {
                return new byte[0];
            }

            @Override
            public byte[] getSignature() {
                return new byte[0];
            }

            @Override
            public String getSigAlgName() {
                return "NONE";
            }

            @Override
            public String getSigAlgOID() {
                return "0.0";
            }

            @Override
            public byte[] getSigAlgParams() {
                return new byte[0];
            }

            @Override
            public boolean[] getIssuerUniqueID() {
                return new boolean[0];
            }

            @Override
            public boolean[] getSubjectUniqueID() {
                return new boolean[0];
            }

            @Override
            public boolean[] getKeyUsage() {
                return new boolean[0];
            }

            @Override
            public int getBasicConstraints() {
                return -1;
            }

            @Override
            public byte[] getEncoded() {
                return new byte[0];
            }

            @Override
            public void verify(PublicKey key) {
            }

            @Override
            public void verify(PublicKey key, String sigProvider) {
            }

            @Override
            public String toString() {
                return "test certificate";
            }

            @Override
            public Set<String> getCriticalExtensionOIDs() {
                return null;
            }

            @Override
            public Set<String> getNonCriticalExtensionOIDs() {
                return null;
            }

            @Override
            public byte[] getExtensionValue(String oid) {
                return null;
            }

            @Override
            public boolean hasUnsupportedCriticalExtension() {
                return false;
            }
        };
    }
}
