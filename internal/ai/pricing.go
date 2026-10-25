package ai

// Price is a model's cost in USD per million tokens.
type Price struct {
	InputPerMTok  float64
	OutputPerMTok float64
}

// Pricing maps provider model IDs to their price.
type Pricing map[string]Price

// Cost returns what usage costs on model, or nil when model has no price.
func (p Pricing) Cost(model string, usage Usage) *float64 {
	price, ok := p[model]
	if !ok {
		return nil
	}
	cost := (float64(usage.InputTokens)*price.InputPerMTok + float64(usage.OutputTokens)*price.OutputPerMTok) / 1e6
	return &cost
}

// WithCost returns usage with its cost on model filled in.
func (p Pricing) WithCost(model string, usage Usage) Usage {
	usage.CostUSD = p.Cost(model, usage)
	return usage
}
