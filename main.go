package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// IDs of Messages that Gmail rejects (i.e., due to unsupported attachments) so we won't retry forwarding.  This
// could be persisted, but would then need to be garbage collected.
var badMessageIDs = make(map[string]struct{})

// Start issuing warning log messages if we've tracked this many bad messages.  You should restart turbogmailify on a regular
// basis to avoid accumulating this much state.
const badMessageLimit = 16 * 1024

// Placeholder Message ID for when an email has no Envelope at the source.
const noValidMessageId = "no-envelope-no-msgid"

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
	Imap    []imapCredentials
	Secrets string
	Tokens  string
}

// Configuration file loaded from JSON.
type config struct {
	Imap    []imapCredentials
	Secrets any
	Tokens  *oauth2.Token
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

// Information needed to connect to an IMAP server. Implicit TLS is mandatory.
type imapCredentials struct {
	Address    string
	Username   string
	Password   string
	Folders    map[string][]string
	IdleFolder string // folder to IDLE on; defaults to "INBOX" if empty
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

	mail, err := gmail.NewService(ctx, option.WithHTTPClient(oa.Client(ctx, c.Tokens)))
	if err != nil {
		log.Fatalln("Unable to create Gmail client:", err)
	}

	// Spin up a goroutine for each IMAP connection.
	for _, imapCreds := range c.Imap {
		go func() {
			for {
				doImapSession(&imapCreds, mail)

				// Error'd out. Cool down and try again.
				time.Sleep(maxPollTime)
			}
		}()
	}

	// Put the main goroutine to sleep.
	log.Println("Startup complete; waiting for mail")
	select {}
}

// Make a new connection to the IMAP server and retrieve and expunge messages.
func doImapSession(imap *imapCredentials, mail *gmail.Service) error {
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
	client, err := imapclient.DialTLS(
		imap.Address, &imapclient.Options{UnilateralDataHandler: dataHandler})
	if err != nil {
		log.Println("Error connecting to IMAP server:", err)
		return err
	}
	defer client.Close()

	// Provide credentials.
	if err := client.
		Login(imap.Username, imap.Password).
		Wait(); err != nil {

		log.Println("LOGIN error:", err)
		return err
	}

	var folders map[string][]string
	if len(imap.Folders) > 0 {
		folders = imap.Folders
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
	var idleFolder string
	if imap.IdleFolder == "" {
		idleFolder = "INBOX"
	} else {
		idleFolder = imap.IdleFolder
	}
	if _, idleFolderExists := folders[idleFolder]; !idleFolderExists {
		// Well, we need to idle on some folder, so just pick one...
		for folder := range folders {
			idleFolder = folder
			break
		}
	}

	orderedFolders := make([]string, 0, len(folders))
	for folder := range folders {
		if folder != idleFolder {
			orderedFolders = append(orderedFolders, folder)
		}
	}
	orderedFolders = append(orderedFolders, idleFolder)

	for {
		// Interrogate each folder and retrieve and expunge everything inside.
		// Idle folder is always last so it remains selected when we enter IDLE.
		for _, folder := range orderedFolders {
			labels := folders[folder]
			for {
				inbox, err := client.
					Select(folder, nil).
					Wait()
				if err != nil {
					log.Println("SELECT error:", err)
					return err
				}

				if inbox.NumMessages <= 0 {
					break
				}

				msg, err := fetchFirstMessage(client, inbox.NumMessages)
				if err != nil {
					return err
				}
				if msg == nil {
					break
				}

				log.Printf(
					"Importing message %s (%s %s #%d, %.1fK)",
					msg.msgId, folder, imap.Username, msg.uid, float32(len(msg.contents))/1024)

				doDelete, err := msg.importToGmail(msg.msgId, mail, labels...)
				if err != nil {
					return err
				}

				if doDelete {
					if err := deleteMessage(client, msg.uid); err != nil {
						return err
					}
				}
			}
		}

		// Go back to sleep until the next mailbox update.
		if err := doIdle(client, mailboxUpdate, maxPollTime); err != nil {
			return err
		}
	}
}

// Retrieve first known-not-bad message from given imap folder (if any)
func fetchFirstMessage(client *imapclient.Client, numMessages uint32) (*message, error) {
	// Index of message to try
	var msgSeqNum uint32 = 1

	// Loop until we find a not-known-bad message or run out of messages
	for {
		var (
			// Set an empty body section to request the raw contents of the entire
			// message.
			entireMessage = []*imap.FetchItemBodySection{{}}
			fetch         = client.Fetch(
				imap.SeqSetNum(msgSeqNum), &imap.FetchOptions{BodySection: entireMessage, UID: true, Envelope: true})
		)
		defer fetch.Close()

		messages, err := fetch.Collect()
		if err != nil {
			log.Println("FETCH error:", err)
			return nil, err
		}

		if len(messages) <= 0 {
			return nil, nil
		}

		var (
			msg   = messages[0]
			msgId string
		)

		if msg.Envelope == nil {
			msgId = noValidMessageId
		} else {
			msgId = msg.Envelope.MessageID
		}

		if _, exists := badMessageIDs[msgId]; exists {
			// log.Printf("  Known bad message %s, skipping", msgId)
		} else {
			var data []byte
			for _, buffer := range msg.BodySection {
				data = append(data, buffer.Bytes...)
			}

			return &message{
				msgId:    msgId,
				uid:      msg.UID,
				contents: data}, nil
		}

		msgSeqNum += 1
		if (msgSeqNum > numMessages) {
		   return nil, nil
		}
	}
}

// An email fetched from an IMAP mailbox.  The contents will be uploaded to Gmail.  The other fields are just for logging
// about the upload.
type message struct {
	msgId    string
	uid      imap.UID
	contents []byte
}

// Return true if the error was logged/handled in here
func maybeHandleGmailError(msgId string, err error) bool {
	var googErr *googleapi.Error
	if errors.As(err, &googErr) {
		// Useful for debugging novel errors from Gmail
		if false {
			log.Printf("Gmail API Error Code: %d\n", googErr.Code)
			log.Printf("Gmail API Error Message: \"%s\"\n", googErr.Message)
			for _, e := range googErr.Errors {
				log.Printf("  + Gmail API Error Reason: \"%s\", Message: \"%s\"\n", e.Reason, e.Message)
			}
		}

		// Check for: "Error 400: Invalid attachment." (https://support.google.com/mail/answer/6590)
		if googErr.Code == 400 && strings.HasPrefix(googErr.Message, "Invalid attachment.") {
			if msgId != noValidMessageId {
				log.Printf("Error: Invalid attachment not allowed by Gmail.  Not forwarding %s.  Ignoring.\n", msgId)
				badMessageIDs[msgId] = struct{}{}
				if count := len(badMessageIDs); count > badMessageLimit {
					log.Print("Warning: More than %d Message-IDs tracked.  Probably time to clean out your mailboxes and restart turbogmailify",
						badMessageLimit)
				}
				return true
			}
		}
	}

	return false
}

// Import this message to Gmail via media upload.  Return (bool, error) to indicate if there
// if the source message should be deleted, and if there was an unhandled error
func (m *message) importToGmail(msgId string, mail *gmail.Service, labels ...string) (bool, error) {
	r, err := mail.Users.Messages.
		Import("me", &gmail.Message{LabelIds: append(labels, "UNREAD")}).
		InternalDateSource("dateHeader").
		NeverMarkSpam(false).
		ProcessForCalendar(true).
		Deleted(false).
		Media(
			bytes.NewReader(m.contents),
			googleapi.ContentType("message/rfc822")).
		Do()
	if err != nil {
		if maybeHandleGmailError(msgId, err) {
			return false, nil
		}
		log.Println("Gmail API Error:", err)
		return false, err
	}

	if r.HTTPStatusCode != 200 {
		err := fmt.Errorf("Gmail returned status code: %v", r.HTTPStatusCode)
		log.Println(err)
		return false, err
	}

	return true, nil
}

// Expunge a message from the inbox by UID.
func deleteMessage(client *imapclient.Client, uid imap.UID) error {
	var (
		setNum     = imap.UIDSetNum(uid)
		addDeleted = &imap.StoreFlags{
			Op:     imap.StoreFlagsAdd,
			Silent: true,
			Flags:  []imap.Flag{imap.FlagDeleted}}
	)
	if err := client.
		Store(setNum, addDeleted, nil).
		Close(); err != nil {

		log.Println("STORE error:", err)
		return err
	}

	if err := client.
		Expunge().
		Close(); err != nil {

		log.Println("EXPUNGE error:", err)
		return err
	}

	return nil
}

// Block until the next mailbox status update, or until the deadline has
// elapsed.
func doIdle(
	client *imapclient.Client,
	mailboxUpdate chan *imapclient.UnilateralDataMailbox,
	deadline time.Duration,
) error {
	idle, err := client.Idle()
	if err != nil {
		log.Println("IDLE error:", err)
		return err
	}

	timer := time.NewTimer(deadline)
	select {
	case <-mailboxUpdate:
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
