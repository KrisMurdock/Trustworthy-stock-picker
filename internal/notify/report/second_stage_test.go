package report

import (
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/firststage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/secondstage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
)

func TestSecondStageArticleIsCopyReadyAndCarriesResearchBoundary(t *testing.T) {
	r := selection.Report{
		Version:          3,
		ID:               "report-id",
		AsOf:             "2026-10-21",
		Target:           "2026-10-22",
		Strategy:         "short_horizon_v1",
		SelectionEnabled: true,
		Adjust:           "qfq",
		PoolSize:         193,
		Eligible:         191,
		FirstStage: &firststage.Report{
			Status:      firststage.StatusOK,
			AsOf:        "2026-10-21",
			Target:      "2026-10-22",
			ModelID:     firststage.ModelID,
			PushEnabled: true,
			PoolSize:    193,
			Eligible:    191,
			Candidates:  []firststage.Candidate{{Rank: 4, Code: "sz300628", Name: "亿联网络"}},
		},
		SecondStage: &secondstage.Report{
			Version:     secondstage.ReportSchemaVersion,
			Status:      secondstage.StatusOK,
			AsOf:        "2026-10-21",
			Target:      "2026-10-22",
			ModelID:     "second_stage_gbdt_v1",
			TopN:        10,
			PushEnabled: true,
			InputCount:  145,
			JEVStatus:   secondstage.JEVStatusNone,
			Degraded:    true,
			Candidates: []secondstage.Candidate{{
				Rank:             1,
				Code:             "sz300628",
				Name:             "亿联网络",
				Sector:           "通信设备",
				FirstStageRank:   4,
				PredictedMaxHigh: 6.82,
				JEVPresent:       false,
				Features: firststage.FeatureSnapshot{
					Return5:        4.2,
					MA20Distance:   3.1,
					RSI14:          61.4,
					VolumeRatio:    1.32,
					High20Distance: -2.5,
					ClosePosition:  0.8,
					TrendScore:     82,
				},
				Reasons: []string{"jev_missing"},
			}},
		},
	}

	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.SecondStage) != 1 {
		t.Fatalf("second-stage articles = %d, want 1", len(cards.SecondStage))
	}
	article := cards.SecondStage[0]
	if article.EventID != "report-id:second-stage:article" || article.PlainText == "" || article.Title == "" {
		t.Fatalf("invalid second-stage article: %+v", article)
	}
	expires := time.Date(2026, 10, 22, 9, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	if !article.ExpiresAt.Equal(expires) {
		t.Fatalf("expiry = %s, want %s", article.ExpiresAt, expires)
	}
	for _, want := range []string{
		"【公众号标题】",
		"【公众号正文】",
		"亿联网络（sz300628）｜通信设备",
		"精排第 1，初筛第 4",
		"模型预测未来 3 日最大高点涨幅为 +6.82%",
		"近 5 日 +4.20%",
		"量比 1.32",
		"JEV 分数缺失",
		"降级",
		"不是买卖信号",
		"不构成任何证券买卖建议",
	} {
		if !strings.Contains(article.PlainText, want) {
			t.Fatalf("article missing %q:\n%s", want, article.PlainText)
		}
	}
	if strings.Contains(article.PlainText, "jev_missing") {
		t.Fatalf("article leaked internal reason codes:\n%s", article.PlainText)
	}
}

func TestSecondStageArticleRequiresExplicitPush(t *testing.T) {
	r := selection.Report{
		Version: 1, ID: "report-id", AsOf: "2026-10-21", Target: "2026-10-22",
		SelectionEnabled: true, Adjust: "qfq", PoolSize: 1,
		SecondStage: &secondstage.Report{
			Version: secondstage.ReportSchemaVersion, Status: secondstage.StatusOK,
			AsOf: "2026-10-21", Target: "2026-10-22", PushEnabled: false,
			Candidates: []secondstage.Candidate{{Rank: 1, Code: "sz000001", Name: "测试"}},
		},
	}
	cards, err := FromSelection(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.SecondStage) != 0 {
		t.Fatalf("unexpected second-stage articles: %+v", cards.SecondStage)
	}
}
