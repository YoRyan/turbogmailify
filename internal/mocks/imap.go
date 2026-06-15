package mocks

import (
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
)

type CommandSelect struct {
	Mailbox string
	Options *imap.SelectOptions
}

type CommandIdle struct{}

type CommandExpunge struct{}

type CommandFetch struct {
	NumSet  imap.NumSet
	Options *imap.FetchOptions
}

type CommandStore struct {
	NumSet  imap.NumSet
	Flags   *imap.StoreFlags
	Options *imap.StoreOptions
}

// A mock IMAP server. All retrieved messages have identical content but
// individual UID's are tracked and deleted.
type TestServer struct {
	imap    *imapserver.Server
	mailbox string
	// The state of the mock message store. Map of mailbox names to slices of
	// UID's.
	Messages map[string]([]uint32)
	// Log of select IMAP commands executed on this server.
	Commands []any
}

func (s *TestServer) CloseServer() error {
	return s.imap.Close()
}

func (s *TestServer) Close() error {
	return nil
}

// Not authenticated state
func (s *TestServer) Login(username string, password string) error {
	return nil
}

// Authenticated state
func (s *TestServer) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	s.Commands = append(s.Commands, CommandSelect{
		Mailbox: mailbox,
		Options: options,
	})
	s.mailbox = mailbox
	return &imap.SelectData{
		NumMessages: uint32(len(s.Messages[mailbox])),
	}, nil
}

func (s *TestServer) Create(mailbox string, options *imap.CreateOptions) error {
	return nil
}

func (s *TestServer) Delete(mailbox string) error {
	return nil
}

func (s *TestServer) Rename(mailbox string, newName string, options *imap.RenameOptions) error {
	panic("not implemented")
}

func (s *TestServer) Subscribe(mailbox string) error {
	return nil
}

func (s *TestServer) Unsubscribe(mailbox string) error {
	return nil
}

func (s *TestServer) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	return nil
}

func (s *TestServer) Status(mailbox string, options *imap.StatusOptions) (*imap.StatusData, error) {
	panic("not implemented")
}

func (s *TestServer) Append(mailbox string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	panic("not implemented")
}

func (s *TestServer) Poll(w *imapserver.UpdateWriter, allowExpunge bool) error {
	return nil
}

func (s *TestServer) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	s.Commands = append(s.Commands, CommandIdle{})
	return nil
}

// Selected state
func (s *TestServer) Unselect() error {
	return nil
}

func (s *TestServer) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	s.Commands = append(s.Commands, CommandExpunge{})
	return nil
}

func (s *TestServer) Search(kind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	panic("not implemented")
}

func (s *TestServer) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	s.Commands = append(s.Commands, CommandFetch{
		NumSet: numSet,
	})

	var uid uint32
	switch v := numSet.(type) {
	case imap.UIDSet:
		panic("not implemented")
	case imap.SeqSet:
		seqs, _ := v.Nums()
		uid = s.Messages[s.mailbox][seqs[0]-1]
	}

	msg := w.CreateMessage(1)
	msg.WriteUID(imap.UID(uid))
	msg.WriteEnvelope(&imap.Envelope{
		Subject: "Hello, World!",
		From: []imap.Address{
			{
				Mailbox: "bob",
				Host:    "example.com",
			},
		},
	})
	body := []byte("Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua.")
	bw := msg.WriteBodySection(&imap.FetchItemBodySection{}, int64(len(body)))
	bw.Write(body)
	bw.Close()
	msg.Close()

	return nil
}

func (s *TestServer) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) error {
	s.Commands = append(s.Commands, CommandStore{
		NumSet:  numSet,
		Flags:   flags,
		Options: options,
	})

	if !(flags.Op == imap.StoreFlagsAdd && len(flags.Flags) == 1 && flags.Flags[0] == imap.FlagDeleted) {
		panic("not implemented")
	}

	toRemove := make(map[uint32]struct{})
	switch v := numSet.(type) {
	case imap.UIDSet:
		nums, _ := v.Nums()
		for _, n := range nums {
			toRemove[uint32(n)] = struct{}{}
		}
	case imap.SeqSet:
	default:
		panic("not implemented")
	}

	if present, ok := s.Messages[s.mailbox]; ok {
		newPresent := make([]uint32, 0)
		for _, n := range present {
			if _, remove := toRemove[n]; !remove {
				newPresent = append(newPresent, n)
			}
		}
		s.Messages[s.mailbox] = newPresent
	}

	return nil
}

func (s *TestServer) Copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	panic("not implemented")
}

func CreateTestServer(messages map[string]([]uint32)) (ts *TestServer, address string) {
	ts = &TestServer{
		Messages: messages,
		Commands: make([]any, 0),
	}
	address = "[::1]:10143"

	options := imapserver.Options{
		NewSession: func(c *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return ts, &imapserver.GreetingData{}, nil
		},
		InsecureAuth: true,
	}
	server := imapserver.New(&options)
	ts.imap = server

	go server.ListenAndServe(address)
	// Block until a successful connection.
	for {
		time.Sleep(time.Millisecond * 10)
		if client, err := imapclient.DialInsecure(address, &imapclient.Options{}); err == nil {
			client.Close()
			break
		}
	}

	return
}
