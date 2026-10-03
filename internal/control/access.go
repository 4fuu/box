package control

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/4fuu/box/internal/event"
	"github.com/4fuu/box/internal/secret"
	"github.com/4fuu/box/internal/store"
	"github.com/4fuu/box/internal/tunnel"
)

// ParseTTL reads a duration. Empty means the token does not expire. A trailing d is days.
func ParseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok && !strings.Contains(days, "d") {
		n, err := strconv.Atoi(days)
		if err != nil || n < 1 || n > 3650 {
			return 0, errors.New("invalid duration")
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < time.Minute {
		return 0, errors.New("invalid duration")
	}
	return d, nil
}

// AddToken creates an access token. The view includes the secret. Do not log it.
func (s *Service) AddToken(comment string, ttl time.Duration) (TokenView, error) {
	comment = strings.TrimSpace(comment)
	if strings.ContainsAny(comment, "\r\n") || len(comment) > 80 {
		return TokenView{}, errors.New("invalid comment")
	}
	if ttl < 0 || (ttl > 0 && ttl < time.Minute) {
		return TokenView{}, errors.New("invalid duration")
	}
	raw, err := secret.AccessToken()
	if err != nil {
		return TokenView{}, err
	}
	var expiry time.Time
	if ttl > 0 {
		expiry = s.Store.Now().Add(ttl)
	}
	row, err := s.Store.AddToken(raw, comment, expiry)
	if err != nil {
		return TokenView{}, err
	}
	return tokenView(row), nil
}

// Tokens lists access tokens, including the secret, oldest first.
func (s *Service) Tokens() ([]TokenView, error) {
	list, err := s.Store.ListTokens()
	if err != nil {
		return nil, err
	}
	out := make([]TokenView, 0, len(list))
	for _, row := range list {
		out = append(out, tokenView(row))
	}
	return out, nil
}

// RemoveToken deletes one access token. A missing id is an error.
func (s *Service) RemoveToken(id int64) error {
	if err := s.Store.DeleteToken(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("token %d not found", id)
		}
		return err
	}
	return nil
}

func tokenView(row store.Token) TokenView {
	return TokenView{
		ID: row.ID, Token: row.Token, Comment: row.Comment,
		Expires: row.Expiry, Created: row.Created,
	}
}

// PublishEvent stores one event and wakes long polls. from is the publisher
// label the server assigns; tokenID records the access token when one
// published. A duplicate key returns the original event with dup set.
func (s *Service) PublishEvent(from string, tokenID *int64, topic string, body []byte, key string) (event.Item, bool, error) {
	if s.Events == nil {
		return event.Item{}, false, errors.New("events are unavailable")
	}
	return s.Events.Publish(from, tokenID, topic, body, key)
}

// ReadEvents queries the log. A Wait blocks until a matching event lands or
// the wait runs out. ctx cancels the wait.
func (s *Service) ReadEvents(ctx context.Context, q event.Query) (event.Result, error) {
	if s.Events == nil {
		return event.Result{}, errors.New("events are unavailable")
	}
	return s.Events.Get(ctx, q)
}

// WireEvent copies a stored event onto the control-stream shape.
func WireEvent(item event.Item) tunnel.EventItem {
	return tunnel.EventItem{
		ID: item.ID, Topic: item.Topic, Body: item.Body, From: item.From, Key: item.Key, Time: item.Time,
	}
}
