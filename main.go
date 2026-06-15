package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const maxPollTime = 5 * time.Minute

func main() {
	ctx := context.Background()

	var (
		doAuth  bool
		useToml bool
	)
	flag.BoolVar(&doAuth, "auth", false, "Request access and refresh tokens from Google instead of processing mail.")
	flag.BoolVar(&useToml, "toml", false, "Process the configuration file as a TOML file, with the Secrets and Tokens sections as strings.")
	flag.Parse()

	if flag.NArg() < 1 {
		log.Fatalln("Did not pass path to configuration file.")
	}

	path := flag.Arg(0)
	cfg := &config{}

	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalln("Failed to open config file:", err)
	}

	if useToml {
		tomlCfg := &tomlConfig{}
		if err := toml.Unmarshal(b, tomlCfg); err != nil {
			log.Fatalln("Failed to parse config file:", err)
		}

		cfg.Imap = tomlCfg.Imap

		if tomlCfg.Secrets != "" {
			if err := json.NewDecoder(strings.NewReader(tomlCfg.Secrets)).Decode(&cfg.Secrets); err != nil {
				log.Fatalln("Unable to decode embedded JSON 'Secrets' section:", err)
			}
		} else {
			log.Fatalln("Failed to parse config file:",
				"Google credentials ('Secrets') are missing")
		}

		if tomlCfg.Tokens != "" {
			if err := json.NewDecoder(strings.NewReader(tomlCfg.Tokens)).Decode(&cfg.Tokens); err != nil {
				log.Fatalln("Unable to decode embedded JSON 'Tokens' section:", err)
			}
		}
	} else {
		if err := json.Unmarshal(b, cfg); err != nil {
			log.Fatalln("Failed to parse config file:", err)
		}
	}

	if len(cfg.Imap) <= 0 {
		log.Fatalln("Failed to parse config file:",
			"IMAP credentials section is empty or not a JSON list")
	}

	if cfg.Secrets == nil {
		log.Fatalln("Failed to parse config file:",
			"Google credentials ('Secrets') are missing")
	}

	if doAuth {
		doRequestAuth(ctx, cfg)
	} else {
		doForwarding(ctx, cfg)
	}
}

// Configuration file loaded from TOML.
type tomlConfig struct {
	Imap    []configImap
	Secrets string
	Tokens  string
}

// Configuration file loaded from JSON.
type config struct {
	Imap    []configImap
	Secrets any
	Tokens  *oauth2.Token
}

type configImap struct {
	Address    string
	Username   string
	Password   string
	Folders    map[string][]string
	IdleFolder string // folder to IDLE on; defaults to "INBOX" if empty
}

func (c *config) getOAuthConfig() (oa *oauth2.Config) {
	// Need to submit the client secret as JSON bytes, leading to this silly
	// re-encode step.
	secrets, err := json.Marshal(c.Secrets)
	if err != nil {
		return
	}

	oa, err = google.ConfigFromJSON(secrets, gmail.GmailInsertScope)
	if err != nil {
		log.Fatalln("Unable to create Google OAuth2 client:", err)
	}
	return
}

// Run in request tokens mode.
func doRequestAuth(ctx context.Context, c *config) {
	oa := c.getOAuthConfig()

	authURL := oa.AuthCodeURL("", oauth2.AccessTypeOffline)
	fmt.Println("Navigate to the following URL in your browser:")
	fmt.Println(authURL)

	var redirectURL string
	fmt.Println("")
	fmt.Println("Once you've authorized the request, your browser will redirect to an http://localhost URL that will fail to load. Paste the entire URL here:")
	if _, err := fmt.Scan(&redirectURL); err != nil {
		log.Fatalln("Unable to read redirected URL:", err)
	}

	parsedURL, err := url.Parse(redirectURL)
	if err != nil {
		log.Fatalln("Unable to parse redirected URL:", err)
	}

	authCode := parsedURL.Query().Get("code")
	if authCode == "" {
		log.Fatalln("URL does not look like it contains a Google code:", "missing 'code' value in query string")
	}

	tokens, err := oa.Exchange(ctx, authCode)
	if err != nil {
		log.Fatalln("Unable to retrieve tokens from Google:", err)
	}

	fmt.Println("")
	fmt.Println("Your 'Tokens' section is as follows:")
	if err := json.NewEncoder(os.Stdout).Encode(tokens); err != nil {
		log.Fatalln("Error converting tokens to JSON:", err)
	}
}

// Run in mail forwarding mode.
func doForwarding(ctx context.Context, c *config) {
	if c.Tokens == nil {
		log.Fatalln("Authorization tokens ('Tokens') are missing. Obtain them with -auth mode.")
	}

	oa := c.getOAuthConfig()

	gmService, err := gmail.NewService(ctx, option.WithHTTPClient(oa.Client(ctx, c.Tokens)))
	if err != nil {
		log.Fatalln("Unable to create Gmail client:", err)
	}
	gmInbox := (*gmailInboxReal)(gmService)

	// Spin up a goroutine for each IMAP connection.
	for _, ic := range c.Imap {
		go func() {
			for {
				s, err := createSession(&ic)
				if err != nil {
					continue
				}

				forwardConfig := createForwardConfig(&ic)
				for {
					if err := s.forwardAndIdle(forwardConfig, gmInbox); err != nil {
						break
					}
				}

				// Error'd out. Cool down and try again.
				time.Sleep(maxPollTime)
			}
		}()
	}

	// Put the main goroutine to sleep.
	log.Println("Startup complete; waiting for mail")
	select {}
}

type forwardConfig struct {
	Id                  string
	FolderToLabels      map[string][]string
	FolderOrderIdleLast []string
}

func createForwardConfig(c *configImap) forwardConfig {
	var folders map[string][]string
	if len(c.Folders) > 0 {
		folders = c.Folders
	} else {
		folders = map[string][]string{
			"INBOX": {"INBOX"},
			"Junk":  {"SPAM"},
		}
	}

	// Determine which folder to IDLE on. It must always be the last folder
	// selected before IDLE — an extra SELECT between the drain loop and IDLE
	// causes some servers (e.g. free.fr) to stop sending push notifications.
	// We achieve this by draining all other folders first and the idle folder
	// last, so no additional SELECT is needed after the loop.
	var idle string
	if c.IdleFolder != "" {
		idle = c.IdleFolder
	} else {
		idle = "INBOX"
	}
	if _, exists := folders[idle]; !exists {
		// Well, we need to idle on some folder, so just pick one...
		for folder := range folders {
			idle = folder
			break
		}
	}

	ordered := make([]string, 0, len(folders))
	for folder := range folders {
		if folder != idle {
			ordered = append(ordered, folder)
		}
	}
	ordered = append(ordered, idle)

	return forwardConfig{
		Id:                  c.Username,
		FolderToLabels:      folders,
		FolderOrderIdleLast: ordered,
	}
}

type session struct {
	client        *imapclient.Client
	mailboxUpdate <-chan *imapclient.UnilateralDataMailbox
	idleDeadline  time.Duration
}

func createSession(c *configImap) (*session, error) {
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
	client, err := imapclient.DialTLS(c.Address, &imapclient.Options{UnilateralDataHandler: dataHandler})
	if err != nil {
		log.Println("Error connecting to IMAP server:", err)
		return nil, err
	}

	// Provide credentials.
	if err := client.
		Login(c.Username, c.Password).
		Wait(); err != nil {

		log.Println("LOGIN error:", err)
		client.Close()
		return nil, err
	}

	return &session{client, mailboxUpdate, maxPollTime}, nil
}

// Make a new connection to the IMAP server and retrieve and expunge messages.
func (s *session) forwardAndIdle(f forwardConfig, gm gmailInbox) error {
	// Interrogate each folder and retrieve and expunge everything inside.
	// Idle folder is always last so it remains selected when we enter IDLE.
	for _, folder := range f.FolderOrderIdleLast {
		labels := f.FolderToLabels[folder]
		for {
			inbox, err := s.client.
				Select(folder, nil).
				Wait()
			if err != nil {
				log.Println("SELECT error:", err)
				return err
			}

			if inbox.NumMessages <= 0 {
				break
			}

			uid, envelope, err := s.fetchFirstMessage()
			if err != nil {
				return err
			}
			if envelope == nil {
				break
			}

			log.Printf(
				"Importing message received by %s (uid %d, size %.1fK, folder %s)",
				f.Id, uid, float32(len(envelope))/1024, folder)

			if err := gm.DoImport(envelope, labels...); err != nil {
				return err
			}

			if err := s.deleteMessage(uid); err != nil {
				return err
			}
		}
	}

	// Go back to sleep until the next mailbox update.
	if err := s.doIdle(); err != nil {
		return err
	}

	return nil
}

// Retrieve inbox message sequence number 1.
func (s *session) fetchFirstMessage() (uid imap.UID, data []byte, err error) {
	var (
		// Set an empty body section to request the raw contents of the entire
		// message.
		entireMessage = []*imap.FetchItemBodySection{{}}
		fetch         = s.client.Fetch(
			imap.SeqSetNum(1), &imap.FetchOptions{BodySection: entireMessage, UID: true})
	)
	defer fetch.Close()

	messages, err := fetch.Collect()
	if err != nil {
		log.Println("FETCH error:", err)
		return
	}

	if len(messages) <= 0 {
		return
	}

	data = make([]byte, 0)
	msg := messages[0]
	uid = msg.UID
	for _, buffer := range msg.BodySection {
		data = append(data, buffer.Bytes...)
	}
	return
}

// A Gmail target that can accept imported messages. This is an interface for
// testing purposes.
type gmailInbox interface {
	DoImport(envelope []byte, labels ...string) error
}

type gmailInboxReal gmail.Service

// Import this message to Gmail via media upload.
func (gm *gmailInboxReal) DoImport(envelope []byte, labels ...string) error {
	r, err := gm.Users.Messages.
		Import("me", &gmail.Message{LabelIds: append(labels, "UNREAD")}).
		InternalDateSource("dateHeader").
		NeverMarkSpam(false).
		ProcessForCalendar(true).
		Deleted(false).
		Media(
			bytes.NewReader(envelope),
			googleapi.ContentType("message/rfc822")).
		Do()
	if err != nil {
		log.Println("Error uploading to Gmail:", err)
		return err
	}

	if r.HTTPStatusCode != 200 {
		err := fmt.Errorf("gmail returned status code: %v", r.HTTPStatusCode)
		log.Println(err)
		return err
	}

	return nil
}

// Expunge a message from the inbox by UID.
func (s *session) deleteMessage(uid imap.UID) error {
	var (
		setNum     = imap.UIDSetNum(uid)
		addDeleted = &imap.StoreFlags{
			Op:     imap.StoreFlagsAdd,
			Silent: true,
			Flags:  []imap.Flag{imap.FlagDeleted}}
	)
	if err := s.client.
		Store(setNum, addDeleted, nil).
		Close(); err != nil {

		log.Println("STORE error:", err)
		return err
	}

	if err := s.client.
		Expunge().
		Close(); err != nil {

		log.Println("EXPUNGE error:", err)
		return err
	}

	return nil
}

// Block until the next mailbox status update, or until the deadline has
// elapsed.
func (s *session) doIdle() error {
	idle, err := s.client.Idle()
	if err != nil {
		log.Println("IDLE error:", err)
		return err
	}

	timer := time.NewTimer(s.idleDeadline)
	select {
	case <-s.mailboxUpdate:
		timer.Stop()
	case <-timer.C:
	}

	if err := idle.Close(); err != nil {
		log.Println("IDLE error:", err)
		return err
	}

	if err := idle.Wait(); err != nil {
		log.Println("IDLE error:", err)
		return err
	}

	return nil
}
