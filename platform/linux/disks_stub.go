//go:build !linux

package linuxplatform

import (
	"context"
	"fmt"

	"nas-server/pkg/disks"
)

func ListCandidates(context.Context) (disks.CandidateList, error) {
	return disks.CandidateList{}, fmt.Errorf("raid candidates not supported on non-linux")
}

func ListDisks(context.Context) (disks.DiskList, error) {
	return disks.DiskList{}, fmt.Errorf("disk details not supported on non-linux")
}
