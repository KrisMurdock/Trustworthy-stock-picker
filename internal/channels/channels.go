// Package channels constructs the configured outbound notification channels.
package channels

import (
	"errors"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/feishu"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
)

// Build creates all enabled channels and returns a cleanup function that closes
// their HTTP clients. The cleanup function is safe to call on every return path.
func Build(c config.Config) ([]notify.Channel, func(), error) {
	specs := c.NotificationChannels()
	out := make([]notify.Channel, 0, len(specs))
	closers := make([]func(), 0, len(specs))
	for _, spec := range specs {
		switch spec.Type {
		case "feishu":
			if spec.Webhook == "" {
				return nil, func() {}, errors.New("enabled Feishu channel requires a webhook")
			}
			if err := feishu.ValidateWebhook(spec.Webhook); err != nil {
				return nil, func() {}, errors.New("enabled Feishu channel has an invalid webhook")
			}
			client := feishu.NewNamedClient(spec.Name, spec.Webhook, spec.Secret, config.Duration(c.Timeout))
			out = append(out, client)
			closers = append(closers, client.Close)
		default:
			return nil, func() {}, errors.New("unsupported notification channel type")
		}
	}
	return out, func() {
		for _, close := range closers {
			close()
		}
	}, nil
}
