package audit

type Category string
type Severity string

const (
	CategoryUser   Category = "user"
	CategorySystem Category = "system"
)

const (
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

type Actor struct {
	UserID      string
	Username    string
	DisplayName string
}

type Entry struct {
	Category     Category
	Severity     Severity
	Source       string
	Event        string
	Action       string
	Success      bool
	Actor        Actor
	IPAddress    string
	IPType       string
	CountryCode  string
	Country      string
	City         string
	UserAgent    string
	Method       string
	Path         string
	ResourceType string
	ResourceID   string
	ResourceName string
	Keyword      string
	Content      string
	KeyData      any
	Message      string
	Details      any
	OccurredAt   string
}

type Log struct {
	ID               string `db:"id" json:"id"`
	Category         string `db:"category" json:"category"`
	Severity         string `db:"severity" json:"severity"`
	Source           string `db:"source" json:"source"`
	Event            string `db:"event" json:"event"`
	Action           string `db:"action" json:"action"`
	Success          bool   `db:"success" json:"success"`
	ActorUserID      string `db:"actor_user_id" json:"actorUserId"`
	ActorUsername    string `db:"actor_username" json:"actorUsername"`
	ActorDisplayName string `db:"actor_display_name" json:"actorDisplayName"`
	IPAddress        string `db:"ip_address" json:"ipAddress"`
	IPType           string `db:"ip_type" json:"ipType"`
	CountryCode      string `db:"country_code" json:"countryCode"`
	Country          string `db:"country" json:"country"`
	City             string `db:"city" json:"city"`
	UserAgent        string `db:"user_agent" json:"userAgent"`
	Method           string `db:"method" json:"method"`
	Path             string `db:"path" json:"path"`
	ResourceType     string `db:"resource_type" json:"resourceType"`
	ResourceID       string `db:"resource_id" json:"resourceId"`
	ResourceName     string `db:"resource_name" json:"resourceName"`
	Keyword          string `db:"keyword" json:"keyword"`
	Content          string `db:"content" json:"content"`
	KeyDataJSON      string `db:"key_data_json" json:"-"`
	Message          string `db:"message" json:"message"`
	DetailsJSON      string `db:"details_json" json:"-"`
	OccurredAt       string `db:"occurred_at" json:"occurredAt"`
	KeyData          any    `json:"keyData,omitempty"`
	Details          any    `json:"details,omitempty"`
}

type ListQuery struct {
	Page        int
	PageSize    int
	Category    string
	Severity    string
	Source      string
	Event       string
	ActorUserID string
	IPAddress   string
	Country     string
	City        string
	Keyword     string
	Success     *bool
	Search      string
	From        string
	To          string
}

type Pagination struct {
	Page       int `json:"page"`
	PageSize   int `json:"pageSize"`
	Total      int `json:"total"`
	TotalPages int `json:"totalPages"`
}

type ListResponse struct {
	Items      []Log      `json:"items"`
	Pagination Pagination `json:"pagination"`
}

type HeatmapBucket struct {
	Time  string `json:"time"`
	Count int    `json:"count"`
}

type HeatmapResponse struct {
	Range   string          `json:"range"`
	Bucket  string          `json:"bucket"`
	Buckets []HeatmapBucket `json:"buckets"`
}
