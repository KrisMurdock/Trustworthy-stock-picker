package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

func applyEnvironment(c *Config) error {
	for name, target := range map[string]*string{
		"STOCK_WATCHER_SECOND_STAGE_BLEND_ID":            &c.SecondStage.BlendID,
		"STOCK_WATCHER_DATA_DIR":                         &c.DataDir,
		"STOCK_WATCHER_WEB_ADDR":                         &c.Web.Listen,
		"STOCK_WATCHER_POOL_FILE":                        &c.PoolFile,
		"STOCK_WATCHER_FEISHU_CREDENTIALS_FILE":          &c.Feishu.CredentialsFile,
		"STOCK_WATCHER_POLL_INTERVAL":                    &c.PollInterval,
		"STOCK_WATCHER_REQUEST_TIMEOUT":                  &c.Timeout,
		"STOCK_WATCHER_ANALYSIS_ENGINE":                  &c.Analysis.Engine,
		"STOCK_WATCHER_ANALYSIS_TIMEOUT":                 &c.Analysis.Timeout,
		"STOCK_WATCHER_ANALYSIS_SYNC_TIMEOUT":            &c.Analysis.SyncTimeout,
		"STOCK_WATCHER_STRUCTURE_GUARD_MODE":             &c.StructureGuard.Mode,
		"STOCK_WATCHER_DAILY_RUN_AT":                     &c.Daily.RunAt,
		"STOCK_WATCHER_DAILY_ADJUST":                     &c.Daily.Adjust,
		"STOCK_WATCHER_CALENDAR_VALID_FROM":              &c.Calendar.ValidFrom,
		"STOCK_WATCHER_CALENDAR_VALID_THROUGH":           &c.Calendar.ValidThrough,
		"STOCK_WATCHER_MARKET_REQUEST_INTERVAL":          &c.Market.RequestInterval,
		"STOCK_WATCHER_MARKET_ATTEMPT_TIMEOUT":           &c.Market.AttemptTimeout,
		"STOCK_WATCHER_MARKET_FAILURE_COOLDOWN":          &c.Market.FailureCooldown,
		"STOCK_WATCHER_MARKET_TUSHARE_TOKEN_ENV":         &c.Market.TushareTokenEnv,
		"STOCK_WATCHER_MARKET_TENCENT_ENDPOINT":          &c.Market.TencentEndpoint,
		"STOCK_WATCHER_MARKET_BAOSTOCK_ADDRESS":          &c.Market.BaostockAddress,
		"STOCK_WATCHER_MARKET_TUSHARE_ENDPOINT":          &c.Market.TushareEndpoint,
		"STOCK_WATCHER_MARKET_EASTMONEY_ENDPOINT":        &c.Market.EastmoneyEndpoint,
		"STOCK_WATCHER_MARKET_SINA_ENDPOINT":             &c.Market.SinaEndpoint,
		"STOCK_WATCHER_MARKET_SINA_DAILY_ENDPOINT":       &c.Market.SinaDailyEndpoint,
		"STOCK_WATCHER_MARKET_LISTING_ENDPOINT":          &c.Market.ListingEndpoint,
		"STOCK_WATCHER_CONTEXT_REQUEST_INTERVAL":         &c.MarketContext.RequestInterval,
		"STOCK_WATCHER_CONTEXT_EASTMONEY_LIST_ENDPOINT":  &c.MarketContext.EastmoneyListEndpoint,
		"STOCK_WATCHER_CONTEXT_EASTMONEY_KLINE_ENDPOINT": &c.MarketContext.EastmoneyKlineEndpoint,
		"STOCK_WATCHER_CONTEXT_TENCENT_ENDPOINT":         &c.MarketContext.TencentEndpoint,
		"STOCK_WATCHER_INTRADAY_POLL_INTERVAL":           &c.Intraday.PollInterval,
		"STOCK_WATCHER_INTRADAY_REQUEST_INTERVAL":        &c.Intraday.RequestInterval,
		"STOCK_WATCHER_INTRADAY_CALIBRATION_AT":          &c.Intraday.CalibrationAt,
		"STOCK_WATCHER_INTRADAY_TENCENT_ENDPOINT":        &c.Intraday.TencentEndpoint,
		"STOCK_WATCHER_PREDICTOR_TRAIN_AT":               &c.Predictor.TrainAt,
		"STOCK_WATCHER_PREDICTOR_WRITE_MODE":             &c.Predictor.WriteMode,
		"STOCK_WATCHER_DAYRANGE_TRAIN_AT":                &c.DayRange.TrainAt,
		"STOCK_WATCHER_DAYRANGE_PREDICT_AT":              &c.DayRange.PredictAt,
		"STOCK_WATCHER_DAYRANGE_RECONCILE_AT":            &c.DayRange.ReconcileAt,
	} {
		if value, ok := os.LookupEnv(name); ok {
			*target = value
		}
	}
	for name, target := range map[string]*bool{
		"STOCK_WATCHER_REQUIRE_CIFS":              &c.RequireCIFS,
		"STOCK_WATCHER_WEB_ENABLED":               &c.Web.Enabled,
		"STOCK_WATCHER_WEB_PUBLIC_READ_ONLY":      &c.Web.PublicReadOnly,
		"STOCK_WATCHER_FEISHU_ENABLED":            &c.Feishu.Enabled,
		"STOCK_WATCHER_INTRADAY_ENABLED":          &c.Intraday.Enabled,
		"STOCK_WATCHER_STRATEGY_ENABLED":          &c.Strategy.Enabled,
		"STOCK_WATCHER_SELECTION_ENABLED":         &c.Selection.Enabled,
		"STOCK_WATCHER_CONTEXT_ENABLED":           &c.MarketContext.Enabled,
		"STOCK_WATCHER_FIRST_STAGE_ENABLED":       &c.FirstStage.Enabled,
		"STOCK_WATCHER_FIRST_STAGE_PUSH_ENABLED":  &c.FirstStage.PushEnabled,
		"STOCK_WATCHER_SECOND_STAGE_ENABLED":      &c.SecondStage.Enabled,
		"STOCK_WATCHER_SECOND_STAGE_PUSH_ENABLED": &c.SecondStage.PushEnabled,
		"STOCK_WATCHER_PREDICTOR_ENABLED":         &c.Predictor.Enabled,
		"STOCK_WATCHER_PREDICTOR_PUSH_ENABLED":    &c.Predictor.PushEnabled,
		"STOCK_WATCHER_DAYRANGE_ENABLED":          &c.DayRange.Enabled,
		"STOCK_WATCHER_DAYRANGE_PUSH_ENABLED":     &c.DayRange.PushEnabled,
		"STOCK_WATCHER_DAYRANGE_STOCK_FEATURES":   &c.DayRange.UseStockFeatures,
		"STOCK_WATCHER_DAYRANGE_MARKET_FEATURES":  &c.DayRange.UseMarketFeatures,
		"STOCK_WATCHER_DAYRANGE_SECTOR_FEATURES":  &c.DayRange.UseSectorFeatures,
	} {
		if value, ok := os.LookupEnv(name); ok {
			parsed, err := strconv.ParseBool(strings.TrimSpace(value))
			if err != nil {
				return errors.New("invalid boolean environment variable: " + name)
			}
			*target = parsed
		}
	}
	for name, target := range map[string]*int{
		"STOCK_WATCHER_DAILY_HISTORY_DAYS":          &c.Daily.HistoryDays,
		"STOCK_WATCHER_FIRST_STAGE_BUDGET":          &c.FirstStage.Budget,
		"STOCK_WATCHER_SECOND_STAGE_TOP_N":          &c.SecondStage.TopN,
		"STOCK_WATCHER_SELECTION_MIN_SCORE":         &c.Selection.MinScore,
		"STOCK_WATCHER_SELECTION_TOP_N":             &c.Selection.TopN,
		"STOCK_WATCHER_SYNC_INCREMENTAL_BARS":       &c.Sync.IncrementalBars,
		"STOCK_WATCHER_SYNC_STATUS_REFRESH_DAYS":    &c.Sync.StatusRefreshDays,
		"STOCK_WATCHER_MARKET_FAILURE_THRESHOLD":    &c.Market.FailureThreshold,
		"STOCK_WATCHER_INTRADAY_WORKERS":            &c.Intraday.Workers,
		"STOCK_WATCHER_CONTEXT_WORKERS":             &c.MarketContext.Workers,
		"STOCK_WATCHER_CONTEXT_HISTORY_LIMIT":       &c.MarketContext.HistoryLimit,
		"STOCK_WATCHER_STRATEGY_MAX_HOLDING_DAYS":   &c.Strategy.MaxHoldingDays,
		"STOCK_WATCHER_STRATEGY_MIN_EVIDENCE":       &c.Strategy.MinEvidence,
		"STOCK_WATCHER_STRATEGY_MIN_TRAINING_DAYS":  &c.Strategy.MinTrainingDays,
		"STOCK_WATCHER_STRATEGY_HOLDOUT_DAYS":       &c.Strategy.HoldoutDays,
		"STOCK_WATCHER_PREDICTOR_MIN_TRAINING_DAYS": &c.Predictor.MinTrainingDays,
		"STOCK_WATCHER_PREDICTOR_VALIDATION_DAYS":   &c.Predictor.ValidationDays,
		"STOCK_WATCHER_PREDICTOR_KEEP_MODELS":       &c.Predictor.KeepModels,
		"STOCK_WATCHER_DAYRANGE_HISTORY_DAYS":       &c.DayRange.HistoryDays,
		"STOCK_WATCHER_DAYRANGE_MIN_TRAIN_DAYS":     &c.DayRange.MinTrainingDays,
		"STOCK_WATCHER_DAYRANGE_CALIBRATION_DAYS":   &c.DayRange.CalibrationDays,
		"STOCK_WATCHER_DAYRANGE_VALIDATION_DAYS":    &c.DayRange.ValidationDays,
		"STOCK_WATCHER_DAYRANGE_KEEP_MODELS":        &c.DayRange.KeepModels,
	} {
		if value, ok := os.LookupEnv(name); ok {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return errors.New("invalid integer environment variable: " + name)
			}
			*target = parsed
		}
	}
	for name, target := range map[string]*float64{
		"STOCK_WATCHER_SECOND_STAGE_FIRST_STAGE_WEIGHT": &c.SecondStage.FirstStageWeight,
		"STOCK_WATCHER_SELECTION_MIN_VOLUME_RATIO":      &c.Selection.MinVolumeRatio,
		"STOCK_WATCHER_SELECTION_RSI_MIN":               &c.Selection.RSIMin,
		"STOCK_WATCHER_SELECTION_RSI_MAX":               &c.Selection.RSIMax,
		"STOCK_WATCHER_SELECTION_MIN_DAY_CHANGE":        &c.Selection.MinDayChange,
		"STOCK_WATCHER_SELECTION_MAX_DAY_CHANGE":        &c.Selection.MaxDayChange,
		"STOCK_WATCHER_SYNC_MIN_COVERAGE_PCT":           &c.Sync.MinCoveragePct,
		"STOCK_WATCHER_SYNC_MIN_ANALYZABLE_PCT":         &c.Sync.MinAnalyzablePct,
		"STOCK_WATCHER_STRATEGY_TARGET_NET_PCT":         &c.Strategy.TargetNetPct,
		"STOCK_WATCHER_STRATEGY_STOP_NET_PCT":           &c.Strategy.StopNetPct,
		"STOCK_WATCHER_STRATEGY_TRADE_CASH":             &c.Strategy.TradeCash,
		"STOCK_WATCHER_STRATEGY_COMMISSION_RATE":        &c.Strategy.CommissionRate,
		"STOCK_WATCHER_STRATEGY_MIN_COMMISSION":         &c.Strategy.MinCommission,
		"STOCK_WATCHER_STRATEGY_TRANSFER_RATE":          &c.Strategy.TransferRate,
		"STOCK_WATCHER_STRATEGY_SELL_STAMP_RATE":        &c.Strategy.SellStampRate,
		"STOCK_WATCHER_STRATEGY_SLIPPAGE_RATE":          &c.Strategy.SlippageRate,
		"STOCK_WATCHER_STRATEGY_MAX_ENTRY_GAP_PCT":      &c.Strategy.MaxEntryGapPct,
		"STOCK_WATCHER_STRATEGY_MIN_RANK_SCORE":         &c.Strategy.MinRankScore,
	} {
		if value, ok := os.LookupEnv(name); ok {
			parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil {
				return errors.New("invalid float environment variable: " + name)
			}
			*target = parsed
		}
	}
	if value, ok := os.LookupEnv("STOCK_WATCHER_SECOND_STAGE_MODELS"); ok {
		models, err := parseModelWeights(value)
		if err != nil {
			return err
		}
		c.SecondStage.Models = models
	}
	c.LLM.Locked = map[string]bool{}
	if c.LLM.Sources == nil {
		c.LLM.Sources = map[string]string{}
	}
	for name, field := range map[string]string{
		"STOCK_WATCHER_LLM_BASE_URL":    "base_url",
		"STOCK_WATCHER_LLM_API_KEY":     "api_key",
		"STOCK_WATCHER_LLM_MODEL":       "model",
		"STOCK_WATCHER_LLM_TIMEOUT":     "timeout",
		"STOCK_WATCHER_LLM_CONFIG_FILE": "config_file",
		"STOCK_WATCHER_LLM_WRITE_MODE":  "write_mode",
	} {
		value, ok := os.LookupEnv(name)
		if !ok {
			continue
		}
		switch field {
		case "base_url":
			c.LLM.BaseURL = value
		case "api_key":
			c.LLM.APIKey = value
		case "model":
			c.LLM.Model = value
		case "timeout":
			c.LLM.Timeout = value
		case "config_file":
			c.LLM.ConfigFile = value
		case "write_mode":
			c.LLM.WriteMode = value
		}
		c.LLM.Locked[field] = true
		c.LLM.Sources[field] = "env"
	}
	if value, ok := os.LookupEnv("STOCK_WATCHER_LLM_ENABLED"); ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return errors.New("invalid boolean environment variable: STOCK_WATCHER_LLM_ENABLED")
		}
		c.LLM.Enabled = parsed
		c.LLM.Locked["enabled"] = true
	}
	if value, ok := os.LookupEnv("STOCK_WATCHER_CALENDAR_HOLIDAYS"); ok {
		c.Calendar.Holidays = splitList(value)
	}
	if value, ok := os.LookupEnv("STOCK_WATCHER_MARKET_PROVIDERS"); ok {
		c.Market.Providers = splitList(value)
	}
	if value, ok := os.LookupEnv("STOCK_WATCHER_NOTIFY_CHANNELS"); ok {
		channels, err := parseEnvironmentChannels(value)
		if err != nil {
			return err
		}
		c.Notify.Channels = channels
	}
	return nil
}

func parseEnvironmentChannels(value string) ([]NotifyChannel, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	entries := strings.Split(value, ";")
	channels := make([]NotifyChannel, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		fields := strings.Split(entry, "|")
		if len(fields) < 2 || len(fields) > 6 {
			return nil, errors.New("invalid STOCK_WATCHER_NOTIFY_CHANNELS")
		}
		channel := NotifyChannel{Name: strings.TrimSpace(fields[0]), Type: strings.TrimSpace(fields[1]), Enabled: true}
		if len(fields) > 2 && strings.TrimSpace(fields[2]) != "" {
			parsed, err := strconv.ParseBool(strings.TrimSpace(fields[2]))
			if err != nil {
				return nil, errors.New("invalid STOCK_WATCHER_NOTIFY_CHANNELS enabled value")
			}
			channel.Enabled = parsed
		}
		if len(fields) > 3 {
			channel.WebhookEnv = strings.TrimSpace(fields[3])
		}
		if len(fields) > 4 {
			channel.SecretEnv = strings.TrimSpace(fields[4])
		}
		if len(fields) > 5 {
			channel.Route = strings.TrimSpace(fields[5])
		}
		channels = append(channels, channel)
	}
	return channels, nil
}

func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
