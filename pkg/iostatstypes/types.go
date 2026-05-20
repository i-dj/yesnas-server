package iostatstypes

import (
	"errors"
	"time"
)

var (
	ErrNotSupported = errors.New("storage io stats not supported")
	ErrTraceBusy    = errors.New("system trace resource busy")
)

type Stats struct {
	StorageID   string    `json:"storageId"`
	StorageType string    `json:"storageType"`
	Platform    string    `json:"platform"`
	Method      string    `json:"method"`
	Scope       string    `json:"scope"`
	MountPath   string    `json:"mountPath"`
	ReadSpeed   float64   `json:"readSpeed"`
	WriteSpeed  float64   `json:"writeSpeed"`
	MeasuredAt  time.Time `json:"measuredAt"`
	Note        string    `json:"note,omitempty"`
	Debug       any       `json:"debug,omitempty"`
}
