package dto

import "github.com/prompt-masters/backend/internal/domain"

type HelperRequest struct {
	Prompt string `json:"prompt"`
	TestID string `json:"test_id"`
}

type HelperResponse struct {
	UsageID          string            `json:"usage_id"`
	HelperType       domain.HelperType `json:"helper_type"`
	Cost             int               `json:"cost"`
	Content          string            `json:"content"`
	RemainingBalance int               `json:"remaining_balance"`
}

type HelperUsageResponse struct {
	UsageID    string              `json:"usage_id"`
	HelperType domain.HelperType   `json:"helper_type"`
	Cost       int                 `json:"cost"`
	Status     domain.HelperStatus `json:"status"`
	Content    *string             `json:"content"`
	ErrorCode  *string             `json:"error_code"`
	CreatedAt  int64               `json:"created_at"`
}

type HelperOverviewResponse struct {
	Balance int                       `json:"balance"`
	Prices  map[domain.HelperType]int `json:"prices"`
	Usage   []HelperUsageResponse     `json:"usage"`
}

func HelperFromDomain(u domain.HelperUsage, balance int) HelperResponse {
	return HelperResponse{UsageID: u.ID.String(), HelperType: u.Type, Cost: u.Cost, Content: u.Content, RemainingBalance: balance}
}

func HelperOverviewFromDomain(balance int, prices map[domain.HelperType]int, usage []domain.HelperUsage) HelperOverviewResponse {
	out := HelperOverviewResponse{Balance: balance, Prices: prices, Usage: make([]HelperUsageResponse, len(usage))}
	for i, u := range usage {
		r := HelperUsageResponse{UsageID: u.ID.String(), HelperType: u.Type, Cost: u.Cost, Status: u.Status, CreatedAt: u.CreatedAt.UnixMilli()}
		if u.Status == domain.HelperDelivered {
			content := u.Content
			r.Content = &content
		}
		if u.ErrorCode != "" {
			code := u.ErrorCode
			r.ErrorCode = &code
		}
		out.Usage[i] = r
	}
	return out
}
