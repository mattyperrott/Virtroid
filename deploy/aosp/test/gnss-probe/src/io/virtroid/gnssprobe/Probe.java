package io.virtroid.gnssprobe;

import android.app.Activity;
import android.location.GnssStatus;
import android.location.Location;
import android.location.LocationListener;
import android.location.LocationManager;
import android.location.OnNmeaMessageListener;
import android.os.Bundle;
import android.util.Log;
import java.util.TreeMap;
import java.util.TreeSet;

// What an app sees of GPS while this is in front, logged under the tag
// GnssProbe for deploy/aleph/test/boot-test.sh: the receiver the system names,
// each fix with the satellites it used, the sky, and which NMEA sentences
// arrive. One line of each kind per fix.
public class Probe extends Activity implements LocationListener {
    private static final String TAG = "GnssProbe";
    private LocationManager lm;
    private final TreeMap<String, Integer> nmea = new TreeMap<>();
    private String lastGga = "";

    private final GnssStatus.Callback sky = new GnssStatus.Callback() {
        @Override
        public void onSatelliteStatusChanged(GnssStatus status) {
            int used = 0;
            TreeSet<Integer> systems = new TreeSet<>();
            for (int i = 0; i < status.getSatelliteCount(); i++) {
                if (status.usedInFix(i)) {
                    used++;
                }
                systems.add(status.getConstellationType(i));
            }
            Log.i(TAG, "sky signals=" + status.getSatelliteCount() + " used=" + used + " systems=" + systems);
        }
    };

    private final OnNmeaMessageListener sentences = (message, timestamp) -> {
        if (message.length() < 7 || message.charAt(0) != '$') {
            nmea.merge("other", 1, Integer::sum);
            return;
        }
        String type = message.substring(3, 6);
        nmea.merge(type, 1, Integer::sum);
        if (type.equals("GGA")) {
            lastGga = message.trim();
        }
    };

    @Override
    protected void onCreate(Bundle b) {
        super.onCreate(b);
        lm = getSystemService(LocationManager.class);
        Log.i(TAG, "receiver name=" + lm.getGnssHardwareModelName() + " year=" + lm.getGnssYearOfHardware()
                + " capabilities=" + lm.getGnssCapabilities());
        Log.i(TAG, "gps enabled=" + lm.isProviderEnabled(LocationManager.GPS_PROVIDER));
        lm.registerGnssStatusCallback(getMainExecutor(), sky);
        lm.addNmeaListener(getMainExecutor(), sentences);
        lm.requestLocationUpdates(LocationManager.GPS_PROVIDER, 1000L, 0f, this);
    }

    @Override
    public void onLocationChanged(Location l) {
        Bundle extras = l.getExtras();
        Log.i(TAG, "fix provider=" + l.getProvider() + " lat=" + l.getLatitude() + " lon=" + l.getLongitude()
                + " acc=" + l.getAccuracy() + " alt=" + l.getAltitude() + " satellites="
                + (extras == null ? "none" : extras.getInt("satellites", -1)) + " mock=" + l.isMock());
        Log.i(TAG, "nmea " + nmea + " gga=" + lastGga);
        nmea.clear();
    }

    @Override
    protected void onDestroy() {
        lm.removeUpdates(this);
        lm.unregisterGnssStatusCallback(sky);
        lm.removeNmeaListener(sentences);
        super.onDestroy();
    }
}
