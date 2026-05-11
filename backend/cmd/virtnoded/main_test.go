package main

import (
	"strings"
	"testing"
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
