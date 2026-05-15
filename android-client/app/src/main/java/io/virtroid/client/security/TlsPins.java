package io.virtroid.client.security;

import java.security.MessageDigest;
import java.security.cert.Certificate;
import java.security.cert.X509Certificate;
import java.util.Arrays;
import java.util.Collections;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import java.util.Base64;

import javax.net.ssl.SSLPeerUnverifiedException;

import okhttp3.CertificatePinner;

public final class TlsPins {
    private static final Map<String, Set<String>> HOST_PINS = buildPins();

    private TlsPins() {
    }

    public static CertificatePinner certificatePinner() {
        CertificatePinner.Builder builder = new CertificatePinner.Builder();
        for (Map.Entry<String, Set<String>> entry : HOST_PINS.entrySet()) {
            builder.add(entry.getKey(), entry.getValue().toArray(new String[0]));
        }
        return builder.build();
    }

    public static boolean hasPinsForHost(String host) {
        return HOST_PINS.containsKey(canonicalHost(host));
    }

    public static void checkPeerCertificates(String host, Certificate[] peerCertificates) throws SSLPeerUnverifiedException {
        Set<String> pins = HOST_PINS.get(canonicalHost(host));
        checkPeerCertificates(host, peerCertificates, pins);
    }

    static void checkPeerCertificates(String host, Certificate[] peerCertificates, Set<String> pins) throws SSLPeerUnverifiedException {
        if (pins == null || pins.isEmpty()) {
            return;
        }
        if (peerCertificates == null || peerCertificates.length == 0) {
            throw new SSLPeerUnverifiedException("pinned TLS host did not present certificates");
        }

        Certificate leaf = peerCertificates[0];
        if (leaf instanceof X509Certificate) {
            String pin = spkiPin((X509Certificate) leaf);
            if (pins.contains(pin)) {
                return;
            }
        }
        throw new SSLPeerUnverifiedException("TLS certificate pin mismatch for " + canonicalHost(host));
    }

    private static Map<String, Set<String>> buildPins() {
        Set<String> virtroidPins = Collections.unmodifiableSet(new HashSet<>(Arrays.asList(
                "sha256/Nc/etmCowzcN39LRJRfDTNr66aWpCdO4dxwBu1gcZ0o=",
                "sha256/a9khLOZJxlnJyrxstg/P+seiDCm+Yf3OsrXyFocBaI0=",
                "sha256/Douxi77vs4G+Ib/BogbTFymEYq0QSFXwSgVCaZcI09Q="
        )));

        Map<String, Set<String>> pins = new HashMap<>();
        pins.put("virtroid.network", virtroidPins);
        pins.put("www.virtroid.network", virtroidPins);
        return Collections.unmodifiableMap(pins);
    }

    private static String canonicalHost(String host) {
        return host == null ? "" : host.trim().toLowerCase(Locale.US);
    }

    private static String spkiPin(X509Certificate certificate) throws SSLPeerUnverifiedException {
        try {
            byte[] digest = MessageDigest.getInstance("SHA-256")
                    .digest(certificate.getPublicKey().getEncoded());
            return "sha256/" + Base64.getEncoder().encodeToString(digest);
        } catch (Exception error) {
            throw new SSLPeerUnverifiedException("could not calculate TLS certificate pin");
        }
    }
}
