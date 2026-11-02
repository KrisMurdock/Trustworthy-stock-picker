package fairvalue

import (
	"context"
	"errors"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

type countingFundamentalSource struct {
	snapshot market.FundamentalSnapshot
	err      error
	calls    int
}

func (s *countingFundamentalSource) FetchFundamentals(context.Context, config.Stock) (market.FundamentalSnapshot, error) {
	s.calls++
	return s.snapshot, s.err
}

func TestAssessFetchesOnceAndReturnsBothMethods(t *testing.T) {
	source := &countingFundamentalSource{snapshot: testSnapshot()}
	estimator := NewEstimator(source)
	assessment, err := estimator.Assess(context.Background(), config.Stock{Code: "sh600519", Sector: "白酒"}, 12, "2026-10-31")
	if err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 {
		t.Fatalf("Assess fetch calls = %d, want 1", source.calls)
	}
	if err := assessment.V1.Validate(); err != nil {
		t.Fatalf("v1 invalid: %v", err)
	}
	if err := assessment.V2.Validate(); err != nil {
		t.Fatalf("v2 invalid: %v", err)
	}
	if !assessment.V1.Available() || !assessment.V2.Available() {
		t.Fatalf("assessment methods unavailable: %+v", assessment)
	}
	if assessment.V1.Code != assessment.V2.Code || assessment.V1.AsOf != assessment.V2.AsOf {
		t.Fatalf("assessment identities diverge: %+v", assessment)
	}

	estimate, err := estimator.Estimate(context.Background(), config.Stock{Code: "sh600519", Sector: "白酒"}, 12, "2026-10-31")
	if err != nil {
		t.Fatal(err)
	}
	if source.calls != 2 {
		t.Fatalf("Estimate added %d fetch calls, want 1", source.calls-1)
	}
	if estimate.Central != assessment.V1.Central || estimate.FairPE != assessment.V1.FairPE || estimate.Confidence != assessment.V1.Confidence {
		t.Fatalf("Estimate result diverges from Assess v1: estimate=%+v assessment=%+v", estimate, assessment.V1)
	}
}

func TestAssessUnavailableInputsMatchEstimateWithoutFetching(t *testing.T) {
	source := &countingFundamentalSource{snapshot: testSnapshot()}
	estimator := NewEstimator(source)
	assessment, err := estimator.Assess(context.Background(), config.Stock{Code: "sh600519"}, 0, "2026-10-31")
	if err != nil {
		t.Fatal(err)
	}
	if source.calls != 0 || assessment.V1.Available() || assessment.V2.Available() ||
		assessment.V1.Reason == "" || assessment.V1.Reason != assessment.V2.Reason {
		t.Fatalf("invalid price assessment diverged: %+v calls=%d", assessment, source.calls)
	}

	assessment, err = (&Estimator{}).Assess(context.Background(), config.Stock{Code: "sh600519"}, 12, "2026-10-31")
	if err != nil {
		t.Fatal(err)
	}
	if assessment.V1.Reason != "财务估值数据源未配置" || assessment.V2.Reason != assessment.V1.Reason {
		t.Fatalf("unconfigured source assessment diverged: %+v", assessment)
	}
	if assessment.V2.DataQuality != DataQualityUnavailable {
		t.Fatalf("unconfigured source v2 quality = %q, want unavailable", assessment.V2.DataQuality)
	}
}

func TestAssessPropagatesSourceFailure(t *testing.T) {
	want := errors.New("upstream unavailable")
	source := &countingFundamentalSource{snapshot: testSnapshot(), err: want}
	assessment, err := NewEstimator(source).Assess(context.Background(), config.Stock{Code: "sh600519"}, 12, "2026-10-31")
	if !errors.Is(err, want) || source.calls != 1 || assessment.V1.Available() || assessment.V2.Available() {
		t.Fatalf("source failure not propagated: err=%v calls=%d assessment=%+v", err, source.calls, assessment)
	}
}
