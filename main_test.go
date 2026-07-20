package main

import (
	"strings"
	"testing"
	"time"

	"github.com/YoRyan/turbogmailify/internal/mocks"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type message struct {
	envelope []byte
	labels   []string
}

type mockInbox struct {
	messages []message
}

func (m *mockInbox) DoImport(envelope []byte, labels ...string) error {
	m.messages = append(m.messages, message{
		envelope: envelope,
		labels:   labels,
	})
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

func slicesEqual[T comparable](t *testing.T, want []T, got []T) {
	lGot := len(got)
	lWant := len(want)
	if lGot != lWant {
		t.Fatalf("len(got) = %d; want %d", lGot, lWant)
	}

	for i, w := range want {
		g := got[i]
		if g != w {
			t.Fatalf("got[%d] = %v; want %v", i, g, w)
		}
	}
}

func TestConfigFromDefault(t *testing.T) {
	fc := createForwardConfig(&configImap{})

	{
		got := len(fc.FolderToLabels)
		if got != 2 {
			t.Fatalf("len(FolderToLabels) = %d; want 2", got)
		}
	}

	{
		got := len(fc.FolderToArchive)
		if got != 0 {
			t.Fatalf("len(FolderToArchive) = %d; want 0", got)
		}
	}

	slicesEqual(t, []string{"INBOX"}, fc.FolderToLabels["INBOX"])
	slicesEqual(t, []string{"SPAM"}, fc.FolderToLabels["Junk"])
	slicesEqual(t, []string{"Junk", "INBOX"}, fc.FolderOrderIdleLast)
}

func TestConfigMissingIdleFolder(t *testing.T) {
	fc := createForwardConfig(&configImap{
		Folders: map[string]([]string){"Junk": []string{"SPAM"}, "INBOX": []string{"INBOX"}},
	})

	got := len(fc.FolderOrderIdleLast)
	if got != 2 {
		t.Fatalf("len(FolderOrderIdleLast) = %d; want 2", got)
	}

	folders := make(map[string]struct{})
	for _, f := range fc.FolderOrderIdleLast {
		folders[f] = struct{}{}
	}
	if _, ok := folders["INBOX"]; !ok {
		t.Fatalf("FolderOrderIdleLast does not contain %s", "INBOX")
	}
	if _, ok := folders["Junk"]; !ok {
		t.Fatalf("FolderOrderIdleLast does not contain %s", "Junk")
	}
}

func TestConfigExplicitIdleFolder(t *testing.T) {
	fc := createForwardConfig(&configImap{
		Folders:    map[string]([]string){"Junk": []string{"SPAM"}, "INBOX": []string{"INBOX"}, "CustomFolder": []string{"CustomLabel"}},
		IdleFolder: "CustomFolder",
	})

	{
		got := len(fc.FolderOrderIdleLast)
		if got != 3 {
			t.Fatalf("len(FolderOrderIdleLast) = %d; want 3", got)
		}
	}

	folders := make(map[string]struct{})
	for _, f := range fc.FolderOrderIdleLast {
		folders[f] = struct{}{}
	}
	if _, ok := folders["INBOX"]; !ok {
		t.Fatalf("FolderOrderIdleLast does not contain %s", "INBOX")
	}
	if _, ok := folders["Junk"]; !ok {
		t.Fatalf("FolderOrderIdleLast does not contain %s", "Junk")
	}

	{
		got := fc.FolderOrderIdleLast[len(fc.FolderOrderIdleLast)-1]
		if got != "CustomFolder" {
			t.Fatalf("last FolderOrderIdleLast = %s; want CustomFolder", got)
		}
	}
}

func TestConfigArchiveDefined(t *testing.T) {
	fc := createForwardConfig(&configImap{
		Folders: map[string]([]string){
			"INBOX": []string{"INBOX"},
			"Junk":  []string{"SPAM"},
		},
		ArchiveFolders: map[string]string{
			"INBOX": "Archive",
			"Junk":  "ArchiveJunk",
		},
	})

	{
		got := len(fc.FolderToArchive)
		if got != 2 {
			t.Fatalf("len(FolderToArchive) = %d; want 2", got)
		}
	}
	{
		got := fc.FolderToArchive["INBOX"]
		if got != "Archive" {
			t.Fatalf("FolderToArchive[INBOX] = %s; want Archive", got)
		}
	}
	{
		got := fc.FolderToArchive["Junk"]
		if got != "ArchiveJunk" {
			t.Fatalf("FolderToArchive[Junk] = %s; want ArchiveJunk", got)
		}
	}
}

func TestConfigArchiveDefinedWithDefaultFolders(t *testing.T) {
	fc := createForwardConfig(&configImap{
		ArchiveFolders: map[string]string{
			"INBOX": "Archive",
			"Junk":  "ArchiveJunk",
		},
	})

	{
		got := len(fc.FolderToArchive)
		if got != 2 {
			t.Fatalf("len(FolderToArchive) = %d; want 2", got)
		}
	}
	{
		got := fc.FolderToArchive["INBOX"]
		if got != "Archive" {
			t.Fatalf("FolderToArchive[INBOX] = %s; want Archive", got)
		}
	}
	{
		got := fc.FolderToArchive["Junk"]
		if got != "ArchiveJunk" {
			t.Fatalf("FolderToArchive[Junk] = %s; want ArchiveJunk", got)
		}
	}
}

func TestConfigArchivePartial(t *testing.T) {
	fc := createForwardConfig(&configImap{
		Folders: map[string]([]string){
			"INBOX": []string{"INBOX"},
			"Junk":  []string{"SPAM"},
		},
		ArchiveFolders: map[string]string{
			"INBOX": "Archive",
		},
	})

	{
		got := len(fc.FolderToArchive)
		if got != 1 {
			t.Fatalf("len(FolderToArchive) = %d; want 1", got)
		}
	}
	{
		got := fc.FolderToArchive["INBOX"]
		if got != "Archive" {
			t.Fatalf("FolderToArchive[INBOX] = %s; want Archive", got)
		}
	}
}

func TestConfigArchivePartialWithFallback(t *testing.T) {
	fc := createForwardConfig(&configImap{
		Folders: map[string]([]string){
			"INBOX": []string{"INBOX"},
			"Junk":  []string{"SPAM"},
		},
		ArchiveFolders: map[string]string{
			"Junk": "ArchiveJunk",
			"*":    "Archive",
		},
	})

	{
		got := len(fc.FolderToArchive)
		if got != 2 {
			t.Fatalf("len(FolderToArchive) = %d; want 2", got)
		}
	}
	{
		got := fc.FolderToArchive["INBOX"]
		if got != "Archive" {
			t.Fatalf("FolderToArchive[INBOX] = %s; want Archive", got)
		}
	}
	{
		got := fc.FolderToArchive["Junk"]
		if got != "ArchiveJunk" {
			t.Fatalf("FolderToArchive[Junk] = %s; want ArchiveJunk", got)
		}
	}
}

func TestConfigArchiveOnlyFallback(t *testing.T) {
	fc := createForwardConfig(&configImap{
		Folders: map[string]([]string){
			"INBOX": []string{"INBOX"},
			"Junk":  []string{"SPAM"},
		},
		ArchiveFolders: map[string]string{
			"*": "Archive",
		},
	})

	{
		got := len(fc.FolderToArchive)
		if got != 2 {
			t.Fatalf("len(FolderToArchive) = %d; want 2", got)
		}
	}
	{
		got := fc.FolderToArchive["INBOX"]
		if got != "Archive" {
			t.Fatalf("FolderToArchive[INBOX] = %s; want Archive", got)
		}
	}
	{
		got := fc.FolderToArchive["Junk"]
		if got != "Archive" {
			t.Fatalf("FolderToArchive[Junk] = %s; want Archive", got)
		}
	}
}

func TestDefaultForward(t *testing.T) {
	ts, addr := mocks.CreateTestServer(map[string]([]uint32){
		"INBOX": []uint32{1},
	}, false)
	defer ts.CloseServer()

	config := &configImap{Address: addr}
	inbox := &mockInbox{}
	if err := createTestSession(config).forwardAndIdle(createForwardConfig(config), inbox); err != nil {
		t.Fatalf("Error executing forwardAndIdle: %v", err)
	}

	{
		got := len(inbox.messages)
		if got != 1 {
			t.Fatalf("len(inbox.messages) = %d; want 1", got)
		}
	}

	msg := inbox.messages[0]
	{
		got := string(msg.envelope)
		if !strings.Contains(got, "Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua.") {
			t.Fatalf("msg.envelope did not contain expected substring; got %s", got)
		}
	}
	{
		got := len(msg.labels)
		if got != 1 {
			t.Fatalf("len(msg.labels) = %d; want 1", got)
		}
	}
	{
		got := msg.labels[0]
		if got != "INBOX" {
			t.Fatalf("msg.labels[0] = %s; want INBOX", got)
		}
	}
}

func TestForwardWithLabels(t *testing.T) {
	ts, addr := mocks.CreateTestServer(map[string]([]uint32){
		"INBOX": []uint32{1},
	}, false)
	defer ts.CloseServer()

	config := &configImap{
		Address: addr,
		Folders: map[string][]string{
			"INBOX": {"Label1", "Label2"},
		},
	}
	inbox := &mockInbox{}
	if err := createTestSession(config).forwardAndIdle(createForwardConfig(config), inbox); err != nil {
		t.Fatalf("Error executing forwardAndIdle: %v", err)
	}

	{
		got := len(inbox.messages)
		if got != 1 {
			t.Fatalf("len(inbox.messages) = %d; want 1", got)
		}
	}

	msg := inbox.messages[0]
	{
		got := len(msg.labels)
		if got != 2 {
			t.Fatalf("len(msg.labels) = %d; want 2", got)
		}
	}
	{
		got := make(map[string](struct{}))
		for _, label := range msg.labels {
			got[label] = struct{}{}
		}
		for _, want := range []string{"Label1", "Label2"} {
			if _, ok := got[want]; !ok {
				t.Fatalf("msg.labels does not contain %s", want)
			}
		}
	}
}

func TestForwardMultipleFolders(t *testing.T) {
	ts, addr := mocks.CreateTestServer(map[string]([]uint32){
		"INBOX":        []uint32{2},
		"CustomFolder": []uint32{1},
	}, false)
	defer ts.CloseServer()

	config := &configImap{
		Address: addr,
		Folders: map[string][]string{
			"INBOX":        {"INBOX"},
			"CustomFolder": {"CustomLabel"},
		},
	}
	inbox := &mockInbox{}
	if err := createTestSession(config).forwardAndIdle(createForwardConfig(config), inbox); err != nil {
		t.Fatalf("Error executing forwardAndIdle: %v", err)
	}

	{
		got := len(inbox.messages)
		if got != 2 {
			t.Fatalf("len(inbox.messages) = %d; want 2", got)
		}
	}

	{
		labels := make(map[string](struct{}))
		for _, msg := range inbox.messages {
			got := len(msg.labels)
			if got != 1 {
				t.Fatalf("len(msg.messages) = %d; want 1", got)
			}

			labels[msg.labels[0]] = struct{}{}
		}

		{
			got := len(labels)
			if got != 2 {
				t.Fatalf("len(labels) = %d; want 2", got)
			}

			for _, want := range []string{"INBOX", "CustomLabel"} {
				if _, ok := labels[want]; !ok {
					t.Fatalf("labels does not contain %s", want)
				}
			}
		}
	}
}

func TestForwardImapCommands(t *testing.T) {
	ts, addr := mocks.CreateTestServer(map[string]([]uint32){
		"INBOX": []uint32{1},
	}, false)
	defer ts.CloseServer()

	config := &configImap{Address: addr}
	inbox := &mockInbox{}
	if err := createTestSession(config).forwardAndIdle(createForwardConfig(config), inbox); err != nil {
		t.Fatalf("Error executing forwardAndIdle: %v", err)
	}

	{
		selects := make([]mocks.CommandSelect, 0)
		for _, cmd := range ts.Commands {
			switch v := cmd.(type) {
			case mocks.CommandSelect:
				selects = append(selects, v)
			}
		}

		{
			got := len(selects)
			if got < 2 {
				t.Fatalf("len(selects) = %d; want >=2", got)
			}
		}

		mailboxes := make(map[string]struct{})
		for _, cmd := range selects {
			mailboxes[cmd.Mailbox] = struct{}{}
		}

		{
			got := len(mailboxes)
			if got != 2 {
				t.Fatalf("len(mailboxes) = %d; want 2", got)
			}
		}

		for _, want := range []string{"Junk", "INBOX"} {
			if _, ok := mailboxes[want]; !ok {
				t.Fatalf("mailboxes does not contain %s", want)
			}
		}
	}

	{
		idleIdx := -1
		for i, cmd := range ts.Commands {
			switch cmd.(type) {
			case mocks.CommandIdle:
				idleIdx = i
			}
		}

		want := len(ts.Commands) - 1
		if idleIdx != want {
			t.Fatalf("idleIdx = %d; want %d", idleIdx, want)
		}
	}

	{
		idleIdx := -1
		for i, cmd := range ts.Commands {
			switch cmd.(type) {
			case mocks.CommandIdle:
				idleIdx = i
			}
		}

		want := len(ts.Commands) - 1
		if idleIdx != want {
			t.Fatalf("idleIdx = %d; want %d", idleIdx, want)
		}
	}

	{
		foundExpunge := false
		for _, cmd := range ts.Commands {
			switch cmd.(type) {
			case mocks.CommandIdle:
				foundExpunge = true
			}
		}

		if !foundExpunge {
			t.Fatalf("IMAP EXPUNGE was not called")
		}
	}

	{
		fetches := make([]mocks.CommandFetch, 0)
		for _, cmd := range ts.Commands {
			switch v := cmd.(type) {
			case mocks.CommandFetch:
				fetches = append(fetches, v)
			}
		}

		{
			got := len(fetches)
			if got != 1 {
				t.Fatalf("len(fetches) = %d; want 1", got)
			}
		}

		fetch := fetches[0]
		{
			got := fetch.NumSet.String()
			if got != "1" {
				t.Fatalf("fetch.NumSet = %s; want 1", got)
			}
		}
	}

	{
		stores := make([]mocks.CommandStore, 0)
		for _, cmd := range ts.Commands {
			switch v := cmd.(type) {
			case mocks.CommandStore:
				stores = append(stores, v)
			}
		}

		{
			got := len(stores)
			if got != 1 {
				t.Fatalf("len(stores) = %d; want 1", got)
			}
		}

		store := stores[0]
		{
			got := store.NumSet.String()
			if got != "1" {
				t.Fatalf("store.NumSet = %s; want 1", got)
			}
		}
		{
			deleteFlag := false
			for _, flag := range store.Flags.Flags {
				if flag == imap.FlagDeleted {
					deleteFlag = true
				}
			}

			if !deleteFlag {
				t.Fatalf("IMAP STORE missing the /Deleted flag")
			}
		}

		if store.Flags.Op == imap.StoreFlagsDel {
			t.Fatalf("IMAP STORE does not set the /Deleted flag")
		}
	}
}

func TestForwardArchiveUsesImapMove(t *testing.T) {
	ts, addr := mocks.CreateTestServer(map[string]([]uint32){
		"INBOX": []uint32{1},
	}, true)
	defer ts.CloseServer()

	config := &configImap{Address: addr, ArchiveFolders: map[string]string{
		"INBOX": "Archive",
	}}
	inbox := &mockInbox{}
	if err := createTestSession(config).forwardAndIdle(createForwardConfig(config), inbox); err != nil {
		t.Fatalf("Error executing forwardAndIdle: %v", err)
	}

	moves := make([]mocks.CommandMove, 0)
	for _, cmd := range ts.Commands {
		switch v := cmd.(type) {
		case mocks.CommandMove:
			moves = append(moves, v)
		}
	}

	{
		got := len(moves)
		if got != 1 {
			t.Fatalf("len(moves) = %d; want 1", got)
		}
	}

	move := moves[0]
	{
		got := move.NumSet.String()
		if got != "1" {
			t.Fatalf("move.NumSet = %s; want 1", got)
		}
	}
	{
		got := move.Dest
		if got != "Archive" {
			t.Fatalf("move.Dest = %s; want Archive", got)
		}
	}
}

func TestForwardArchiveUsesImapCopy(t *testing.T) {
	ts, addr := mocks.CreateTestServer(map[string]([]uint32){
		"INBOX": []uint32{1},
	}, false)
	defer ts.CloseServer()

	config := &configImap{Address: addr, ArchiveFolders: map[string]string{
		"INBOX": "Archive",
	}}
	inbox := &mockInbox{}
	if err := createTestSession(config).forwardAndIdle(createForwardConfig(config), inbox); err != nil {
		t.Fatalf("Error executing forwardAndIdle: %v", err)
	}

	copies := make([]mocks.CommandCopy, 0)
	for _, cmd := range ts.Commands {
		switch v := cmd.(type) {
		case mocks.CommandCopy:
			copies = append(copies, v)
		}
	}

	{
		got := len(copies)
		if got != 1 {
			t.Fatalf("len(copies) = %d; want 1", got)
		}
	}

	copy := copies[0]
	{
		got := copy.NumSet.String()
		if got != "1" {
			t.Fatalf("copy.NumSet = %s; want 1", got)
		}
	}
	{
		got := copy.Dest
		if got != "Archive" {
			t.Fatalf("copy.Dest = %s; want Archive", got)
		}
	}
}

func TestFolderSelectOrderMatchesConfig(t *testing.T) {
	ts, addr := mocks.CreateTestServer(map[string]([]uint32){
		"INBOX":        []uint32{1},
		"Junk":         []uint32{2},
		"CustomFolder": []uint32{3},
	}, false)
	defer ts.CloseServer()

	config := &configImap{
		Address:    addr,
		IdleFolder: "INBOX",
	}
	inbox := &mockInbox{}
	fc := createForwardConfig(config)
	if err := createTestSession(config).forwardAndIdle(fc, inbox); err != nil {
		t.Fatalf("Error executing forwardAndIdle: %v", err)
	}

	mailboxes := make([]string, 0)
	for _, cmd := range ts.Commands {
		switch v := cmd.(type) {
		case mocks.CommandSelect:
			mailboxes = append(mailboxes, v.Mailbox)
		}
	}

	var (
		distinct = make([]string, 0)
		head     = ""
	)
	for _, s := range mailboxes {
		if s != head {
			distinct = append(distinct, s)
			head = s
		}
	}
	slicesEqual(t, fc.FolderOrderIdleLast, distinct)
}

func TestIdleFolderSelectIsLast(t *testing.T) {
	ts, addr := mocks.CreateTestServer(map[string]([]uint32){
		"INBOX":        []uint32{1},
		"Junk":         []uint32{2},
		"CustomFolder": []uint32{3},
	}, false)
	defer ts.CloseServer()

	config := &configImap{
		Address:    addr,
		IdleFolder: "INBOX",
	}
	inbox := &mockInbox{}
	if err := createTestSession(config).forwardAndIdle(createForwardConfig(config), inbox); err != nil {
		t.Fatalf("Error executing forwardAndIdle: %v", err)
	}

	mailboxes := make([]string, 0)
	for _, cmd := range ts.Commands {
		switch v := cmd.(type) {
		case mocks.CommandSelect:
			mailboxes = append(mailboxes, v.Mailbox)
		}
	}

	got := mailboxes[len(mailboxes)-1]
	if got != config.IdleFolder {
		t.Fatalf("last mailbox = %s; want %s", got, config.IdleFolder)
	}
}
