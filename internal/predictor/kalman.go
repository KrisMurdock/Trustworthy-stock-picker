package predictor

import (
	"math"
	"time"
)

type KalmanParams struct {
	ProcessLevel   float64
	ProcessSlope   float64
	Measurement    float64
	TrendThreshold float64
}

func DefaultKalmanParams() KalmanParams {
	return KalmanParams{ProcessLevel: 1e-6, ProcessSlope: 1e-9, Measurement: 1e-4, TrendThreshold: 0.4}
}

type KalmanState struct {
	Code      string    `json:"code"`
	Date      string    `json:"date"`
	BarEnd    time.Time `json:"bar_end"`
	Level     float64   `json:"level"`
	Slope     float64   `json:"slope"`
	P00       float64   `json:"p00"`
	P01       float64   `json:"p01"`
	P11       float64   `json:"p11"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Kalman struct {
	Params      KalmanParams
	State       KalmanState
	Initialized bool
}

func NewKalman(params KalmanParams) *Kalman {
	if params.TrendThreshold <= 0 {
		params = DefaultKalmanParams()
	}
	return &Kalman{Params: params}
}

func KalmanFromState(state KalmanState, params KalmanParams) *Kalman {
	if params.TrendThreshold <= 0 {
		params = DefaultKalmanParams()
	}
	return &Kalman{Params: params, State: state, Initialized: true}
}

func (k *Kalman) Update(code, date string, at time.Time, close float64) KalmanState {
	observation := math.Log(close)
	if !k.Initialized {
		k.State = KalmanState{Code: code, Date: date, BarEnd: at, Level: observation, Slope: 0,
			P00: 1, P01: 0, P11: 1e-4, UpdatedAt: at.UTC()}
		k.Initialized = true
		return k.State
	}
	level := k.State.Level + k.State.Slope
	p00 := k.State.P00 + 2*k.State.P01 + k.State.P11 + k.Params.ProcessLevel
	p01 := k.State.P01 + k.State.P11
	p11 := k.State.P11 + k.Params.ProcessSlope
	innovation := observation - level
	s := p00 + k.Params.Measurement
	k0 := p00 / s
	k1 := p01 / s
	level += k0 * innovation
	slope := k.State.Slope + k1*innovation
	p00 -= k0 * p00
	p01 -= k0 * p01
	p11 -= k1 * p01
	if p00 < 0 {
		p00 = 0
	}
	if p11 < 0 {
		p11 = 0
	}
	k.State = KalmanState{Code: code, Date: date, BarEnd: at, Level: level, Slope: slope,
		P00: p00, P01: p01, P11: p11, UpdatedAt: at.UTC()}
	return k.State
}

func (k *Kalman) Trend() (state string, confidence string, score float64) {
	if !k.Initialized {
		return "震荡", "低", 0
	}
	score = k.State.Slope / math.Sqrt(math.Max(k.State.P11, 1e-12))
	switch {
	case score >= 2*k.Params.TrendThreshold:
		return "偏强", "高", score
	case score >= k.Params.TrendThreshold:
		return "偏强", "中", score
	case score <= -2*k.Params.TrendThreshold:
		return "偏弱", "高", score
	case score <= -k.Params.TrendThreshold:
		return "偏弱", "中", score
	default:
		return "震荡", "低", score
	}
}
