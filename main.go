package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/YoRyan/turbogmailify/internal/mocks"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const (
	maxPollTime      = 5 * time.Minute
	networkRetryTime = maxPollTime
)

const maxMessagesInOneBatch = 32

func main() {
	var (
		doAuth   bool
		useToml  bool
		logLevel int
	)
	flag.BoolVar(&doAuth, "auth", false, "Request access and refresh tokens from Google instead of processing mail.")
	flag.BoolVar(&useToml, "toml", false, "Process the configuration file as a TOML file, with the Secrets and Tokens sections as strings.")
	flag.IntVar(&logLevel, "log", int(slog.LevelInfo), "Change the log level, which defaults to INFO (0). See https://pkg.go.dev/log/slog#Level.")
	flag.Parse()

	// Use TextHandler to filter out control codes and avoid console logging attacks.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.Level(logLevel)})))

	if flag.NArg() < 1 {
		slog.Error("Did not pass path to configuration file.")
		os.Exit(1)
	}

	path := flag.Arg(0)
	cfg := &config{}

	b, err := os.ReadFile(path)
	if err != nil {
		slog.Error("Failed to open config file", "err", err)
		os.Exit(1)
	}

	if useToml {
		tomlCfg := &tomlConfig{}
		if err := toml.Unmarshal(b, tomlCfg); err != nil {
			slog.Error("Failed to parse config file", "err", err)
			os.Exit(1)
		}

		cfg.Imap = tomlCfg.Imap

		if tomlCfg.Secrets != "" {
			if err := json.NewDecoder(strings.NewReader(tomlCfg.Secrets)).Decode(&cfg.Secrets); err != nil {
				slog.Error("Unable to decode embedded JSON 'Secrets' section", "err", err)
				os.Exit(1)
			}
		} else {
			slog.Error("Failed to parse config file",
				"err", "Google credentials ('Secrets') are missing")
			os.Exit(1)
		}

		if tomlCfg.Tokens != "" {
			if err := json.NewDecoder(strings.NewReader(tomlCfg.Tokens)).Decode(&cfg.Tokens); err != nil {
				slog.Error("Unable to decode embedded JSON 'Tokens' section", "err", err)
				os.Exit(1)
			}
		}
	} else {
		if err := json.Unmarshal(b, cfg); err != nil {
			slog.Error("Failed to parse config file", "err", err)
			os.Exit(1)
		}
	}

	if len(cfg.Imap) <= 0 {
		slog.Error("Failed to parse config file",
			"err", "IMAP credentials section is empty or not a JSON list")
		os.Exit(1)
	}

	if cfg.Secrets == nil {
		slog.Error("Failed to parse config file",
			"err", "Google credentials ('Secrets') are missing")
		os.Exit(1)
	}

	if doAuth {
		doRequestAuth(cfg)
	} else {
		doForwarding(cfg)
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
	Address            string
	Username           string
	Password           string
	Folders            map[string][]string
	ArchiveFolders     map[string]string
	FailedFolders      map[string]string
	NeverMarkSpam      map[string]bool
	ProcessForCalendar map[string]bool
	IdleFolder         string // folder to IDLE on; defaults to "INBOX" if empty
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
		slog.Error("Unable to create Google OAuth2 client", "err", err)
		os.Exit(1)
	}
	return
}

// Run in request tokens mode.
func doRequestAuth(c *config) {
	oa := c.getOAuthConfig()

	authURL := oa.AuthCodeURL("", oauth2.AccessTypeOffline)
	fmt.Println("Navigate to the following URL in your browser:")
	fmt.Println(authURL)

	var redirectURL string
	fmt.Println("")
	fmt.Println("Once you've authorized the request, your browser will redirect to an http://localhost URL that will fail to load. Paste the entire URL here:")
	if _, err := fmt.Scan(&redirectURL); err != nil {
		slog.Error("Unable to read redirected URL", "err", err)
		os.Exit(1)
	}

	parsedURL, err := url.Parse(redirectURL)
	if err != nil {
		slog.Error("Unable to parse redirected URL", "err", err)
		os.Exit(1)
	}

	authCode := parsedURL.Query().Get("code")
	if authCode == "" {
		slog.Error("URL does not look like it contains a Google code", "err", "missing 'code' value in query string")
		os.Exit(1)
	}

	tokens, err := oa.Exchange(context.Background(), authCode)
	if err != nil {
		slog.Error("Unable to retrieve tokens from Google", "err", err)
		os.Exit(1)
	}

	fmt.Println("")
	fmt.Println("Your 'Tokens' section is as follows:")
	if err := json.NewEncoder(os.Stdout).Encode(tokens); err != nil {
		slog.Error("Error converting tokens to JSON", "err", err)
		os.Exit(1)
	}
}

// Run in mail forwarding mode.
func doForwarding(c *config) {
	if c.Tokens == nil {
		slog.Error("Authorization tokens ('Tokens') are missing. Obtain them with -auth mode.")
		os.Exit(1)
	}

	oa := c.getOAuthConfig()

	ctx := context.Background()
	gmService, err := gmail.NewService(ctx, option.WithHTTPClient(oa.Client(ctx, c.Tokens)))
	if err != nil {
		slog.Error("Unable to create Gmail client", "err", err)
		os.Exit(1)
	}
	gmInbox := (*gmailInboxReal)(gmService)

	// Spin up a goroutine for each IMAP connection.
	for _, ic := range c.Imap {
		go doForwardingForImap(&ic, gmInbox)
	}

	// Put the main goroutine to sleep.
	slog.Info("Startup complete; waiting for mail")
	select {}
}

// Forward mail indefinitely for a configured IMAP account.
func doForwardingForImap(c *configImap, in gmailInbox) {
	forwardConfig := createForwardConfig(c)
	importFailedUids := forwardConfig.createUidMap[struct{}]()
	for {
		func() {
			s, err := connectSession(c, &forwardConfig, importFailedUids)
			if err != nil {
				time.Sleep(networkRetryTime)
				return
			}
			defer s.close()

			for {
				cooldown, err := s.forwardAndIdle(in)
				time.Sleep(cooldown)
				if err != nil {
					return
				}
			}
		}()
	}
}

type forwardConfig struct {
	id                         string
	folderToLabels             map[string][]string
	folderToArchive            map[string]string
	folderToFailed             map[string]string
	folderToNeverMarkSpam      map[string]bool
	folderToProcessForCalendar map[string]bool
	folderOrderIdleLast        []string
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
		id:             c.Username,
		folderToLabels: folders,
		// Reusing ordered is more convenient than iterating through all the
		// keys in folders just to produce a slice.
		folderToArchive: folderMap[string]{m: c.ArchiveFolders, folders: ordered}.wildcard().done(),
		folderToFailed:  folderMap[string]{m: c.FailedFolders, folders: ordered}.wildcard().done(),
		folderToNeverMarkSpam: folderMap[bool]{m: c.NeverMarkSpam, folders: ordered}.def(map[string]bool{
			"Junk": true,
		}).wildcard().fallback(false).done(),
		folderToProcessForCalendar: folderMap[bool]{m: c.ProcessForCalendar, folders: ordered}.def(map[string]bool{
			"Junk": false,
		}).wildcard().fallback(true).done(),
		folderOrderIdleLast: ordered,
	}
}

// Initialize a nested map that accepts (mailbox, uid) tuples. For now, we
// ignore UID validity, as no use case of this structure survives restarts.
func (f forwardConfig) createUidMap[V any]() map[string]map[imap.UID]V {
	m := make(map[string]map[imap.UID]V, len(f.folderOrderIdleLast))
	for _, folder := range f.folderOrderIdleLast {
		m[folder] = make(map[imap.UID]V, 0)
	}
	return m
}

// A map of IMAP folders to configuration values that includes a list of all
// possible keys (folders).
type folderMap[V any] struct {
	m       map[string]V
	folders []string
}

// Specifies a default map if the user has not already configured one.
func (fm folderMap[V]) def(def map[string]V) folderMap[V] {
	var m map[string]V
	if len(fm.m) <= 0 {
		m = def
	} else {
		m = fm.m
	}
	folders := fm.folders
	return folderMap[V]{m, folders}
}

// Adds the wildcard (*) value to the map, which populates a value for all
// folders that do not already have explicit values.
func (fm folderMap[V]) wildcard() folderMap[V] {
	wildcard, isWildcard := fm.m["*"]
	m := make(map[string]V, len(fm.folders))
	for _, k := range fm.folders {
		if v, ok := fm.m[k]; ok {
			m[k] = v
		} else if isWildcard {
			m[k] = wildcard
		}
	}
	folders := fm.folders
	return folderMap[V]{m, folders}
}

// Specifies a fallback value so that every folder is mapped to something.
func (fm folderMap[V]) fallback(fb V) folderMap[V] {
	m := make(map[string]V, len(fm.folders))
	for _, k := range fm.folders {
		if v, ok := fm.m[k]; ok {
			m[k] = v
		} else {
			m[k] = fb
		}
	}
	folders := fm.folders
	return folderMap[V]{m, folders}
}

// Finalizes this structure into an ordinary map.
func (fm folderMap[V]) done() map[string]V {
	return fm.m
}

type session struct {
	client           *imapclient.Client
	config           *forwardConfig
	mailboxUpdate    <-chan *imapclient.UnilateralDataMailbox
	idleDeadline     time.Duration
	importFailedUids map[string]map[imap.UID]struct{}
}

func connectSession(c *configImap, f *forwardConfig, importFailedUids map[string]map[imap.UID]struct{}) (*session, error) {
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
		slog.Error("Error connecting to IMAP server", "err", err)
		return nil, err
	}

	// Provide credentials.
	if err := client.
		Login(c.Username, c.Password).
		Wait(); err != nil {

		slog.Error("LOGIN error", "err", err)
		client.Close()
		return nil, err
	}

	return &session{client, f, mailboxUpdate, maxPollTime, importFailedUids}, nil
}

// Make a new connection to the IMAP server and retrieve and process messages.
func (s *session) forwardAndIdle(gm gmailInbox) (cooldown time.Duration, err error) {
	var unfetchedMessages = false // any folder with more messages?

	// Interrogate each folder and retrieve and process everything inside.
	// Idle folder is always last so it remains selected when we enter IDLE.
	fc := s.config
	for _, folder := range fc.folderOrderIdleLast {
		labels := fc.folderToLabels[folder]

		// SELECT picks the desired folder and also comes with a message count.
		inbox, err := s.client.
			Select(folder, nil).
			Wait()
		if err != nil {
			slog.Error("SELECT error", "err", err)
			return networkRetryTime, err
		}

		// Handle a bunch of imports (up to maxMessagesInOneBatch)
		// as a single step before moving or expunging any
		// messages. These operations affect the sequence numbers.

		messageCount := min(maxMessagesInOneBatch, inbox.NumMessages)
		if messageCount < inbox.NumMessages {
			unfetchedMessages = true
		}

		var (
			successUids = make([]imap.UID, 0)
			failUids    = make([]imap.UID, 0)
		)
		failFolder, failFolderOk := fc.folderToFailed[folder]

		for i := range messageCount {
			uid, envelope, err := s.fetchMessage(i + 1)
			if err != nil {
				return networkRetryTime, err
			}

			if _, failed := s.importFailedUids[folder][uid]; failed {
				// Skip anything on the in-memory blacklist.
				continue
			}

			sum := messageSummary(envelope)
			slog.Info("Importing message", "account", fc.id, "folder", folder, "size", sum.size, "subject", sum.subject)

			if err := gm.DoImport(envelope, fc.folderToNeverMarkSpam[folder], fc.folderToProcessForCalendar[folder], labels...); err != nil {
				slog.Error("Error importing message", "err", err)
				if isImportRetryable(err) {
					// Leave it alone; try again next cycle.
				} else {
					failUids = append(failUids, uid)

					var disposition string
					if failFolderOk {
						disposition = fmt.Sprintf("The message has been moved to %s, the configured IMAP folder for failed messages.", failFolder)
					} else {
						disposition = "The message will not be uploaded again until the next restart of the service. It is recommended that you designate an IMAP folder to move failed messages into so they will not be retried again. This can be accomplished with the FailedFolders option."
					}
					body := executeTemplate(
						template.Must(template.New("body").Parse(`<table>
<tr><td>Error:</td><td>{{ .Error }}</td></tr>
<tr><td>Account:</td><td>{{ .Account }}</td></tr>
<tr><td>Folder:</td><td>{{ .Folder }}</td></tr>
<tr><td>Subject:</td><td>{{ .Subject }}</td></tr>
<tr><td>Message-ID:</td><td>{{ .MessageID }}</td></tr>
<tr><td>Size:</td><td>{{ .Size }}</td></tr>
</table>
<p>{{ .Disposition }}</p>`)),
						map[string]string{
							"Error":       err.Error(),
							"Account":     fc.id,
							"Folder":      folder,
							"Size":        sum.size,
							"Disposition": disposition,
							// Make sure to escape these values properly. They come from directly from raw email headers.
							"Subject":   sum.subject,
							"MessageID": sum.messageId,
						})
					sendHtmlNotification(gm, fmt.Sprintf("Message Import Failure: %s (%s)", fc.id, folder), body)
				}
			} else {
				successUids = append(successUids, uid)
			}
		}

		if dest, ok := fc.folderToArchive[folder]; ok {
			// Move to the configured archive folder.
			for _, uid := range successUids {
				if err := s.moveMessage(uid, dest); err != nil {
					return networkRetryTime, err
				}
			}
		} else {
			// Expunge
			for _, uid := range successUids {
				if err := s.deleteMessage(uid); err != nil {
					return networkRetryTime, err
				}
			}
		}

		if failFolderOk {
			// Move to the configured failed folder.
			for _, uid := range failUids {
				if err := s.moveMessage(uid, failFolder); err != nil {
					return networkRetryTime, err
				}
			}
		} else {
			// Add to the in-memory blacklist.
			for _, uid := range failUids {
				s.importFailedUids[folder][uid] = struct{}{}
			}
		}
	}

	// Unless a big folder has more messages, sleep until the idle mailbox updates.
	if !unfetchedMessages {
		var cooldown time.Duration
		cooldown, err = s.doIdle()
		time.Sleep(cooldown)
	}
	if err != nil {
		return
	}

	// No need for a cooldown of our own. We already blocked on doIdle() and
	// consumed its cooldown.
	return
}

// Retrieve one message from an inbox by sequence number.
func (s *session) fetchMessage(seq uint32) (uid imap.UID, data []byte, err error) {
	var (
		// Set an empty body section to request the raw contents of the entire
		// message.
		entireMessage = []*imap.FetchItemBodySection{{}}
		fetch         = s.client.Fetch(
			imap.SeqSetNum(seq), &imap.FetchOptions{BodySection: entireMessage, UID: true})
	)
	defer fetch.Close()

	messages, err := fetch.Collect()
	if err != nil {
		slog.Error("FETCH error", "err", err)
		return
	}

	nMessages := len(messages)
	if nMessages != 1 {
		err = fmt.Errorf("invalid number of fetch message buffers: %d", nMessages)
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

type summary struct {
	size      string
	subject   string
	messageId string
}

func messageSummary(envelope []byte) summary {
	size := fmt.Sprintf("%.1fK", float32(len(envelope))/1024)
	msg, err := mail.ReadMessage(bytes.NewReader(envelope))
	if err == nil {
		return summary{
			size:      size,
			subject:   msg.Header.Get("Subject"),
			messageId: msg.Header.Get("Message-ID"),
		}
	} else {
		return summary{
			size: size,
		}
	}
}

// Fully close out the session's resources.
func (s *session) close() (err error) {
	if err = s.client.Close(); err != nil {
		return
	}
	return
}

// A Gmail target that can accept imported messages. This is an interface for
// testing purposes.
type gmailInbox interface {
	DoImport(envelope []byte, neverMarkSpam, processForCalendar bool, labels ...string) error
}

type gmailInboxReal gmail.Service

// Import this message to Gmail via media upload.
func (gm *gmailInboxReal) DoImport(envelope []byte, neverMarkSpam, processForCalendar bool, labels ...string) error {
	r, err := gm.Users.Messages.
		Import("me", &gmail.Message{LabelIds: append(labels, "UNREAD")}).
		InternalDateSource("dateHeader").
		NeverMarkSpam(neverMarkSpam).
		ProcessForCalendar(processForCalendar).
		Deleted(false).
		Media(
			bytes.NewReader(envelope),
			googleapi.ContentType("message/rfc822")).
		Do()
	if err != nil {
		slog.Error("Error uploading to Gmail", "err", err)
		return err
	}

	if r.HTTPStatusCode != 200 {
		err := fmt.Errorf("gmail returned status code: %v", r.HTTPStatusCode)
		slog.Error("Gmail upload returned HTTP status code", "code", r.HTTPStatusCode)
		return err
	}

	return nil
}

func isImportRetryable(err error) bool {
	if gerr, ok := errors.AsType[*googleapi.Error](err); ok {
		// The following API errors are known:
		//   400: Invalid attachment (prohibited .exe attachment)
		return gerr.Code != 400
	}

	if errors.Is(err, mocks.ErrNonRetryable) {
		return false
	}

	return true
}

func executeTemplate(tmpl *template.Template, data any) string {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return err.Error()
	} else {
		return buf.String()
	}
}

func sendHtmlNotification(gm gmailInbox, subject, body string) error {
	envelope := fmt.Sprintf(`From: Turbogmailify <me>
To: me
Subject: %s
Content-Type: text/html

<html><body>
%s
</body></html>
`, subject, body)
	return gm.DoImport([]byte(envelope), true, false, "INBOX")
}

// Move a message into another mailbox using IMAP MOVE or, if that command is
// not available, COPY/STORE/EXPUNGE.
func (s *session) moveMessage(uid imap.UID, mailbox string) error {
	if _, err := s.client.
		Move(imap.UIDSetNum(uid), mailbox).
		Wait(); err != nil {

		slog.Error("MOVE error", "err", err)
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

		slog.Error("STORE error", "err", err)
		return err
	}

	if err := s.client.
		Expunge().
		Close(); err != nil {

		slog.Error("EXPUNGE error", "err", err)
		return err
	}

	return nil
}

// Block until the next mailbox status update, or until the deadline has
// elapsed.
func (s *session) doIdle() (cooldown time.Duration, err error) {
	idle, err := s.client.Idle()
	if err != nil {
		// Probably means the server doesn't support IDLE.
		slog.Debug("IDLE error", "err", err)
		return s.idleDeadline, nil
	}

	timer := time.NewTimer(s.idleDeadline)
	select {
	case <-s.mailboxUpdate:
		timer.Stop()
	case <-timer.C:
	}

	// Any errors past this point should have occurred after idling or waiting,
	// so no need for a cooldown.

	if err = idle.Close(); err != nil {
		slog.Debug("IDLE Close error", "err", err)
		return
	}

	if err = idle.Wait(); err != nil {
		slog.Debug("IDLE Wait error", "err", err)
		return
	}

	return
}
