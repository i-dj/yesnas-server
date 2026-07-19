package system

import "testing"

func TestParseTurbostatOutput(t *testing.T) {
	telemetry := parseTurbostatOutput("PkgTmp PkgWatt\n47 8.36\n")
	if telemetry.temperatureC == nil || *telemetry.temperatureC != 47 {
		t.Fatalf("temperatureC = %v, want 47", telemetry.temperatureC)
	}
	if telemetry.powerW == nil || *telemetry.powerW != 8.4 {
		t.Fatalf("powerW = %v, want 8.4", telemetry.powerW)
	}
}

func TestParseTurbostatOutputMissingValues(t *testing.T) {
	telemetry := parseTurbostatOutput("Linux turbostat\nBusy% Bzy_MHz\n1.2 900\n")
	if telemetry.temperatureC != nil || telemetry.powerW != nil {
		t.Fatalf("telemetry = %#v, want empty", telemetry)
	}
}
