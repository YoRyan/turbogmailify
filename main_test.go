package main

import (
	"strings"
	"testing"
	"time"

	"github.com/YoRyan/turbogmailify/internal/mocks"
	"github.com/emersion/go-imap/v2/imapclient"
)

type mockInbox struct {
	envelope []byte
	labels   []string
}

func (m *mockInbox) DoImport(envelope []byte, labels ...string) error {
	m.envelope = envelope
	m.labels = labels
	return nil
}

func createTestSession(c *configImap) *session {
	// Make a handler and channel to receive mailbox status updates.
	var (
		mailboxUpdate = make(chan *imapclient.UnilateralDataMailbox)
		dataHandler   = &imapclient.UnilateralDataHandler{
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				select {
				case mailboxUpdate <- data:
				default:
				}
			}}
	)

	// Open the connection.
	client, _ := imapclient.DialInsecure(c.Address, &imapclient.Options{UnilateralDataHandler: dataHandler})

	// Provide credentials.
	client.
		Login(c.Username, c.Password).
		Wait()

	return &session{
		client, mailboxUpdate, time.Duration(0),
	}
}

func TestDefaultForward(t *testing.T) {
	ts, addr := mocks.CreateTestServer(map[string]([]uint32){
		"INBOX": []uint32{1},
	})
	config := &configImap{Address: addr}
	inbox := &mockInbox{}
	if err := createTestSession(config).forwardAndIdle(createForwardConfig(config), inbox); err != nil {
		t.Fatalf("Error executing forwardAndIdle: %v", err)
	}

	{
		got := string(inbox.envelope)
		if !strings.Contains(got, "Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua.") {
			t.Fatalf("inbox.envelope did not contain expected substring; got %s", got)
		}
	}
	{
		got := len(inbox.labels)
		if got != 1 {
			t.Fatalf("len(inbox.labels) = %d; want 1", got)
		}
	}
	{
		got := inbox.labels[0]
		if got != "INBOX" {
			t.Fatalf("inbox.labels[0] = %s; want INBOX", got)
		}
	}

	if err := ts.CloseServer(); err != nil {
		t.Errorf("Error closing test server: %v", err)
	}
}
