package mocks

import "errors"

type Message struct {
	Envelope []byte
	Labels   []string
}

type MockInbox struct {
	Messages []Message
}

func (m *MockInbox) DoImport(envelope []byte, labels ...string) error {
	m.Messages = append(m.Messages, Message{
		Envelope: envelope,
		Labels:   labels,
	})
	return nil
}

var (
	ErrRetryable    = errors.New("retryable import error")
	ErrNonRetryable = errors.New("non-retryable import error")
)

type ErrorInbox struct {
	Returns  error
	Messages []Message
}

func (e *ErrorInbox) DoImport(envelope []byte, labels ...string) error {
	e.Messages = append(e.Messages, Message{
		Envelope: envelope,
		Labels:   labels,
	})
	return e.Returns
}
