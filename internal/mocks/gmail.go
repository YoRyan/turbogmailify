package mocks

import "errors"

type Message struct {
	Envelope           []byte
	Labels             []string
	NeverMarkSpam      bool
	ProcessForCalendar bool
}

type MockInbox struct {
	Messages []Message
}

func (m *MockInbox) DoImport(envelope []byte, neverMarkSpam, processForCalendar bool, labels ...string) error {
	m.Messages = append(m.Messages, Message{
		Envelope:           envelope,
		Labels:             labels,
		NeverMarkSpam:      neverMarkSpam,
		ProcessForCalendar: processForCalendar,
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

func (e *ErrorInbox) DoImport(envelope []byte, neverMarkSpam, processForCalendar bool, labels ...string) error {
	e.Messages = append(e.Messages, Message{
		Envelope:           envelope,
		Labels:             labels,
		NeverMarkSpam:      neverMarkSpam,
		ProcessForCalendar: processForCalendar,
	})
	return e.Returns
}
