package system

import monitor "nas-server/pkg/iostats"

type StorageStatsError struct {
	StorageID string `json:"storageId"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

type StorageStatsBatch struct {
	Items  []monitor.Stats     `json:"items"`
	Errors []StorageStatsError `json:"errors,omitempty"`
}
