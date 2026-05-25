package main

import (
	"strings"
	"testing"

	"virtroid/backend/internal/config"
)

func TestNormalizeViewerPrepareParamsDefaultsAndBounds(t *testing.T) {
	maxSize, bitRate, err := normalizeViewerPrepareParams(0, 0, runtimeAssignment{
		WidthPx:  720,
		HeightPx: 1600,
	})
	if err != nil {
		t.Fatalf("normalizeViewerPrepareParams returned error: %v", err)
	}
	if maxSize != 1600 {
		t.Fatalf("maxSize = %d, want 1600", maxSize)
	}
	if bitRate != viewerDefaultBitRate {
		t.Fatalf("bitRate = %d, want %d", bitRate, viewerDefaultBitRate)
	}
}

func TestNormalizeViewerPrepareParamsRejectsOutOfRangeValues(t *testing.T) {
	if _, _, err := normalizeViewerPrepareParams(viewerMaxMaxSize+1, viewerDefaultBitRate, runtimeAssignment{}); err == nil || !strings.Contains(err.Error(), "max_size") {
		t.Fatalf("oversized max_size error = %v, want max_size rejection", err)
	}
	if _, _, err := normalizeViewerPrepareParams(viewerDefaultMaxSize, viewerMaxBitRate+1, runtimeAssignment{}); err == nil || !strings.Contains(err.Error(), "bit_rate") {
		t.Fatalf("oversized bit_rate error = %v, want bit_rate rejection", err)
	}
}

func TestRuntimeAppsToInstallMergesDefaultsAndSelections(t *testing.T) {
	node := &nodeAgent{
		cfg: config.NodeConfig{
			DefaultAppPackages: []string{"org.fdroid.basic", "projekt.launcher"},
		},
	}
	apps := node.runtimeAppsToInstall(runtimeAssignment{
		SelectedApps: []runtimeApp{
			{PackageName: "org.fdroid.basic", DisplayName: "F-Droid Basic"},
			{PackageName: "org.videolan.vlc", DisplayName: "VLC"},
		},
	})
	if len(apps) != 3 {
		t.Fatalf("len(apps) = %d, want 3: %+v", len(apps), apps)
	}
	if apps[0].PackageName != "org.fdroid.basic" || apps[0].APKURL == "" || apps[0].APKSHA256 == "" {
		t.Fatalf("first default app = %+v, want pinned F-Droid Basic metadata", apps[0])
	}
	if apps[1].PackageName != "projekt.launcher" {
		t.Fatalf("second app = %+v, want hyperion launcher default", apps[1])
	}
	if apps[2].PackageName != "org.videolan.vlc" {
		t.Fatalf("third app = %+v, want selected VLC after defaults", apps[2])
	}
}
