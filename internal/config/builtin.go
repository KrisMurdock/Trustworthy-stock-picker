package config

// Builtin returns the production configuration. Deployment-specific values
// remain overridable through environment variables.
func Builtin() Config {
	c := Defaults()
	c.Version = 1
	c.Strategy.Version = 1
	c.Strategy.Enabled = true
	c.PoolFile = "leaders.yaml"
	c.Selection.Enabled = true
	c.Feishu.Enabled = true
	c.Predictor.Enabled = true
	c.Predictor.PushEnabled = true
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.Calendar.Holidays = []string{
		"2026-01-01",
		"2026-01-02",
		"2026-02-16",
		"2026-02-17",
		"2026-02-18",
		"2026-02-19",
		"2026-02-20",
		"2026-02-23",
		"2026-04-06",
		"2026-05-01",
		"2026-05-04",
		"2026-05-05",
		"2026-06-19",
		"2026-09-25",
		"2026-10-01",
		"2026-10-02",
		"2026-10-05",
		"2026-10-06",
		"2026-10-07",
	}
	return c
}
