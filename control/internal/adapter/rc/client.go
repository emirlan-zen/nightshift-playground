// Package rc adapts the scoped nightshift-rc executable to application ports.
package rc

import (
	"context"
	"strings"
)

type ExecuteFunc func(context.Context, string, ...string) (string, error)

type Client struct {
	wrapper string
	execute ExecuteFunc
}

func NewClient(wrapper string, execute ExecuteFunc) *Client {
	return &Client{wrapper: wrapper, execute: execute}
}

func (c *Client) Run(ctx context.Context, action, target string) (string, error) {
	output, err := c.execute(ctx, "sudo", c.wrapper, action, target)
	return strings.TrimSpace(output), err
}
