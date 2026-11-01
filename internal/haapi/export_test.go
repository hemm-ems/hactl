package haapi

import (
	"context"
	"encoding/json"
)

// SendCommandForTest sends any websocket type through the generic sender, so
// an external test can prove an unmapped type is refused at the gate (H-29).
func (ws *WSClient) SendCommandForTest(ctx context.Context, cmdType string, params map[string]any) (json.RawMessage, error) {
	return ws.sendCommand(ctx, cmdType, params)
}

// PostForTest sends a POST to any path through the retrying builder.
func (c *Client) PostForTest(ctx context.Context, path string, body any) ([]byte, error) {
	return c.doPost(ctx, path, body)
}
