package client

import (
	"context"
	"fmt"
	"net/url"

	"go.sia.tech/core/types"
	"go.sia.tech/renterd/alerts"
)

// Alerts fetches the active alerts from the bus.
func (c *Client) Alerts(opts alerts.AlertsOpts) (alerts []alerts.Alert, err error) {
	values := url.Values{}
	if opts.Offset != 0 {
		values.Set("offset", fmt.Sprint(opts.Offset))
	}
	if opts.Limit != 0 {
		values.Set("limit", fmt.Sprint(opts.Limit))
	}
	err = c.c.GET("/alerts?"+values.Encode(), &alerts)
	return
}

// DismissAlerts dimisses the alerts with the given IDs.
func (c *Client) DismissAlerts(ctx context.Context, ids ...types.Hash256) error {
	return c.dismissAlerts(ctx, false, ids...)
}

// DismissAllAlerts dimisses all registered alerts.
func (c *Client) DismissAllAlerts(ctx context.Context) error {
	return c.dismissAlerts(ctx, true)
}

func (c *Client) dismissAlerts(ctx context.Context, all bool, ids ...types.Hash256) error {
	values := url.Values{}
	if all {
		values.Set("all", fmt.Sprint(true))
	}
	return c.c.WithContext(ctx).POST("/alerts/dismiss?"+values.Encode(), ids, nil)
}

// RegisterAlert registers the given alert.
func (c *Client) RegisterAlert(ctx context.Context, alert alerts.Alert) error {
	return c.c.WithContext(ctx).POST("/alerts/register", alert, nil)
}
