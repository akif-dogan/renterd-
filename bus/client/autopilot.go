package client

import (
	"context"

	"go.sia.tech/renterd/api"
)

type UpdateAutopilotOption func(*api.UpdateAutopilotRequest)

func WithAutopilotEnabled(enabled bool) UpdateAutopilotOption {
	return func(req *api.UpdateAutopilotRequest) {
		req.Enabled = &enabled
	}
}
func WithContractsConfig(cfg api.ContractsConfig) UpdateAutopilotOption {
	return func(req *api.UpdateAutopilotRequest) {
		req.Contracts = &cfg
	}
}
func WithCurrentPeriod(currentPeriod uint64) UpdateAutopilotOption {
	return func(req *api.UpdateAutopilotRequest) {
		req.CurrentPeriod = &currentPeriod
	}
}
func WithHostsConfig(cfg api.HostsConfig) UpdateAutopilotOption {
	return func(req *api.UpdateAutopilotRequest) {
		req.Hosts = &cfg
	}
}

// Autopilot returns the autopilot.
func (c *Client) Autopilot(ctx context.Context) (ap api.Autopilot, err error) {
	err = c.c.WithContext(ctx).GET("/autopilot", &ap)
	return
}

// UpdateAutopilot updates the autopilot.
func (c *Client) UpdateAutopilot(ctx context.Context, opts ...UpdateAutopilotOption) error {
	var req api.UpdateAutopilotRequest
	for _, opt := range opts {
		opt(&req)
	}
	return c.c.WithContext(ctx).PUT("/autopilot", req)
}

// UpdateCurrentPeriod updates the current period.
func (c *Client) UpdateCurrentPeriod(ctx context.Context, currentPeriod uint64) error {
	return c.UpdateAutopilot(ctx, WithCurrentPeriod(currentPeriod))
}
