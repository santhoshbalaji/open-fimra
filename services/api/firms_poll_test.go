package main

import (
	"slices"
	"testing"
	"time"
)

func TestFIRMSPollSettings(t *testing.T) {
	t.Setenv("FIMRA_FIRMS_POLL_INTERVAL", "")
	t.Setenv("FIMRA_FIRMS_POLL_PRODUCTS", "")
	defaults, err := readFIRMSPollSettings()
	want := []string{"VIIRS_NOAA20_NRT", "VIIRS_NOAA21_NRT", "MODIS_NRT"}
	if err != nil || defaults.interval != 5*time.Minute || !slices.Equal(defaults.products, want) {
		t.Fatalf("FIRMS defaults = %+v: %v", defaults, err)
	}

	t.Setenv("FIMRA_FIRMS_POLL_PRODUCTS", "MODIS_NRT, VIIRS_NOAA21_NRT")
	t.Setenv("FIMRA_FIRMS_POLL_INTERVAL", "1m")
	configured, err := readFIRMSPollSettings()
	if err != nil || configured.interval != time.Minute || !slices.Equal(configured.products, []string{"MODIS_NRT", "VIIRS_NOAA21_NRT"}) {
		t.Fatalf("configured poll settings = %+v: %v", configured, err)
	}
	for _, interval := range []string{"59s", "0s", "invalid"} {
		t.Setenv("FIMRA_FIRMS_POLL_INTERVAL", interval)
		if _, err := readFIRMSPollSettings(); err == nil {
			t.Fatalf("accepted invalid interval %q", interval)
		}
	}
	for _, products := range []string{"VIIRS_SNPP_NRT", "VIIRS_NOAA20_SP", "MODIS_NRT,MODIS_NRT", "MODIS_NRT,"} {
		if _, err := firmsPollProducts(products); err == nil {
			t.Fatalf("accepted invalid live products %q", products)
		}
	}
}
