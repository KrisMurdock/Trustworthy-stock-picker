// Package maexec implements the frozen adjusted-return research model. Quantities
// are synthetic fractional units, never historical shares or executable orders.
package maexec

type Config struct {
	Capital       float64 `json:"capital"`
	Fraction      float64 `json:"position_fraction"`
	MaxPositions  int     `json:"max_positions"`
	Participation float64 `json:"participation"`
	Tick          float64 `json:"tick"`
	Holding       int     `json:"holding_sessions"`
	Target        float64 `json:"target"`
	Stop          float64 `json:"stop"`
	Slippage      float64 `json:"slippage"`
	RiskFree      float64 `json:"annual_risk_free"`
}

func DefaultConfig() Config {
	return Config{Capital: 200000, Fraction: .1, MaxPositions: 10, Participation: .001, Tick: .000001, Holding: 20, Target: .05, Stop: .05, Slippage: .001}
}

type Provenance struct {
	Calendar    string `json:"calendar"`
	Rules       string `json:"execution_rules"`
	Assumptions string `json:"assumptions"`
}
type Signal struct {
	Code string  `json:"code"`
	Date string  `json:"date"`
	ATR  float64 `json:"atr"`
}
type Bar struct {
	Open        float64 `json:"open"`
	High        float64 `json:"high"`
	Low         float64 `json:"low"`
	Close       float64 `json:"close"`
	MA10        float64 `json:"ma10_raw"`
	Assumed     bool    `json:"availability_assumed"`
	Observation string  `json:"observation"`
	Buy         bool    `json:"can_buy_open"`
	Sell        bool    `json:"can_sell_open"`
	Intraday    bool    `json:"can_sell_intraday"`
	Lower       float64 `json:"limit_down"`
	Upper       float64 `json:"limit_up"`
	Commission  float64 `json:"commission"`
	Minimum     float64 `json:"min_commission"`
	Transfer    float64 `json:"transfer"`
	Stamp       float64 `json:"stamp"`
	ADV         float64 `json:"adv20_prior"`
}
type Input struct {
	Version    int                       `json:"schema_version"`
	PriceBasis string                    `json:"price_basis"`
	Model      string                    `json:"execution_model"`
	Provenance Provenance                `json:"provenance"`
	Config     Config                    `json:"config"`
	Sessions   []string                  `json:"sessions"`
	Signals    []Signal                  `json:"signals"`
	Bars       map[string]map[string]Bar `json:"bars"`
}
type Position struct {
	Entry      float64 `json:"entry"`
	Shares     float64 `json:"shares"`
	Cost       float64 `json:"cost"`
	BuyFees    float64 `json:"buy_fees"`
	EntryIndex int     `json:"entry_index"`
	EntryDate  string  `json:"entry_date"`
	SignalDate string  `json:"signal_date"`
	Stop       float64 `json:"stop"`
	Target     float64 `json:"target"`
	Pending    *string `json:"pending"`
}

func (p *Position) queue(reason string) {
	if p.Pending == nil {
		p.Pending = &reason
	}
}

type Decision struct {
	Signal
	Status string `json:"status"`
}
type Order struct {
	Date   string  `json:"date"`
	Code   string  `json:"code"`
	Side   string  `json:"side"`
	Fill   float64 `json:"fill"`
	Shares float64 `json:"shares"`
	Fees   float64 `json:"fees"`
	Reason string  `json:"reason,omitempty"`
}
type Trade struct {
	Code       string  `json:"code"`
	SignalDate string  `json:"signal_date"`
	EntryDate  string  `json:"entry_date"`
	ExitDate   string  `json:"exit_date"`
	Reason     string  `json:"reason"`
	Shares     float64 `json:"shares"`
	EntryPrice float64 `json:"entry_price"`
	ExitPrice  float64 `json:"exit_price"`
	BuyCost    float64 `json:"buy_cost"`
	Proceeds   float64 `json:"sell_proceeds"`
	Fees       float64 `json:"total_fees"`
	Income     float64 `json:"corporate_income"`
	PNL        float64 `json:"net_pnl"`
	Return     float64 `json:"net_return"`
	Holding    int     `json:"holding_sessions"`
}
type Equity struct {
	Date       string  `json:"date"`
	Cash       float64 `json:"cash"`
	Value      float64 `json:"market_value"`
	Equity     float64 `json:"equity"`
	Receivable float64 `json:"dividend_receivable"`
	Positions  int     `json:"positions"`
}
type Metrics struct {
	Closed     int      `json:"closed_trades"`
	WinRate    *float64 `json:"net_win_rate"`
	Expectancy *float64 `json:"net_expectancy_amount"`
	MeanReturn *float64 `json:"mean_net_return"`
	PF         *float64 `json:"profit_factor"`
	Gains      float64  `json:"gross_net_gains"`
	Losses     float64  `json:"gross_net_losses"`
	Return     float64  `json:"portfolio_return"`
	Drawdown   float64  `json:"max_drawdown"`
	Sharpe     *float64 `json:"sharpe_252_rf"`
	Ambiguous  int      `json:"ambiguous_sell_days"`
	Stale      int      `json:"stale_valuation_position_days"`
}
type Result struct {
	Version       int                  `json:"schema_version"`
	Model         string               `json:"execution_model"`
	PriceBasis    string               `json:"price_basis"`
	QuantityBasis string               `json:"quantity_basis"`
	Config        Config               `json:"config"`
	Decisions     []Decision           `json:"decisions"`
	Orders        []Order              `json:"orders"`
	Trades        []Trade              `json:"trades"`
	NAV           []Equity             `json:"daily_equity"`
	Open          map[string]*Position `json:"open_positions"`
	Metrics       Metrics              `json:"metrics"`
	Limitations   []string             `json:"limitations"`
}
