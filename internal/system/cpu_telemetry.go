package system

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
)

func readCPUTelemetry(ctx context.Context) cpuTelemetry {
	path, err := exec.LookPath("turbostat")
	if err != nil {
		return cpuTelemetry{}
	}

	args := []string{"--Summary", "--quiet", "--show", "PkgTmp,PkgWatt", "--interval", "1", "--num_iterations", "1"}
	commands := []*exec.Cmd{
		exec.CommandContext(ctx, "sudo", append([]string{"-n", path}, args...)...),
		exec.CommandContext(ctx, path, args...),
	}
	for _, cmd := range commands {
		output, err := cmd.Output()
		if err != nil {
			continue
		}
		telemetry := parseTurbostatOutput(string(output))
		if telemetry.temperatureC != nil || telemetry.powerW != nil {
			return telemetry
		}
	}
	return cpuTelemetry{}
}

func parseTurbostatOutput(output string) cpuTelemetry {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	tempIndex, powerIndex := -1, -1
	for _, line := range lines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		if tempIndex < 0 && powerIndex < 0 {
			for index, field := range fields {
				switch field {
				case "PkgTmp", "CoreTmp":
					tempIndex = index
				case "PkgWatt":
					powerIndex = index
				}
			}
			continue
		}

		telemetry := cpuTelemetry{}
		if tempIndex >= 0 && tempIndex < len(fields) {
			telemetry.temperatureC = parseTurbostatFloat(fields[tempIndex])
		}
		if powerIndex >= 0 && powerIndex < len(fields) {
			telemetry.powerW = parseTurbostatFloat(fields[powerIndex])
		}
		if telemetry.temperatureC != nil || telemetry.powerW != nil {
			return telemetry
		}
	}
	return cpuTelemetry{}
}

func parseTurbostatFloat(value string) *float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || parsed < 0 {
		return nil
	}
	parsed = round1(parsed)
	return &parsed
}
