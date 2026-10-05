package creght

import (
	"context"
)

// User 是当前登录的 creght 用户。
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

func (c *Client) Self(ctx context.Context) (*User, error) {
	var u User
	if err := c.Get(ctx, "/api/p/self", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}
