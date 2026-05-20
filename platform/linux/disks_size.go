//go:build linux

package linuxplatform

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func readBlockDeviceSizeBytes(kernelName string) (uint64, error) {
	content, err := os.ReadFile(filepath.Join("/sys/class/block", kernelName, "size"))
	if err != nil {
		return 0, err
	}

	sectors, err := strconv.ParseUint(strings.TrimSpace(string(content)), 10, 64)
	if err != nil {
		return 0, err
	}
	sectorSize := readSectorSize(kernelName)
	if sectorSize == 0 {
		return 0, fmt.Errorf("invalid sector size")
	}
	return sectors * sectorSize, nil
}
