package fairvalue

import (
	"context"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

// Assessment contains both valuation methods calculated from one fundamental
// fetch, allowing callers to compare v1 and v2 without duplicating upstream
// traffic.
type Assessment struct {
	V1 Estimate   `json:"v1"`
	V2 EstimateV2 `json:"v2"`
}

// Assess fetches the fundamental snapshot once and returns both estimates. Its
// unavailable-input behavior matches Estimate: missing configuration or invalid
// inputs return an explained, unavailable result without an error, while source
// failures are propagated.
func (e *Estimator) Assess(ctx context.Context, stock config.Stock, price float64, asOf string) (Assessment, error) {
	out := Assessment{
		V1: Estimate{Version: Version, Code: stock.Code, AsOf: asOf, Source: "sina", Method: Method},
		V2: EstimateV2{
			Version: VersionV2, Code: stock.Code, AsOf: asOf, Source: "sina",
			Method: MethodV2, DataQuality: DataQualityUnavailable,
		},
	}
	if e == nil || e.Source == nil {
		out.V1.Reason = "财务估值数据源未配置"
		out.V2.Reason = out.V1.Reason
		return out, nil
	}
	if !config.ValidCode(stock.Code) || price <= 0 || !finite(price) {
		out.V1.Reason = "收盘价或股票代码无效"
		out.V2.Reason = out.V1.Reason
		return out, nil
	}
	snapshot, err := e.Source.FetchFundamentals(ctx, stock)
	if err != nil {
		return out, err
	}
	out.V1 = Calculate(stock, price, asOf, snapshot)
	out.V2 = CalculateV2(stock, price, asOf, snapshot)
	return out, nil
}
