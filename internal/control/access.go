package control

import (
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

// PublishEvent appends one line. from is the publisher label the server assigns.
func (s *Service) PublishEvent(from, topic, body string) (event.Item, error) {
	if s.Events == nil {
		return event.Item{}, errors.New("events are unavailable")
	}
	return s.Events.Publish(from, topic, body)
}

// ReadEvents returns lines newer than since. topic empty means every topic.
func (s *Service) ReadEvents(since int64, topic string) ([]event.Item, error) {
	if s.Events == nil {
		return nil, errors.New("events are unavailable")
	}
	return s.Events.Since(since, topic), nil
}

// WireEvent copies a log line onto the control-stream shape.
func WireEvent(item event.Item) tunnel.EventItem {
	return tunnel.EventItem{
		ID: item.ID, Topic: item.Topic, Body: item.Body, From: item.From, Time: item.Time,
	}
}
